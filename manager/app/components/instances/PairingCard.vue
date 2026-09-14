<script setup lang="ts">
import QRCodeStyling from 'qr-code-styling'
import { ApiError } from '~/composables/useApi'
import type { ConnectResult, InstanceStatus } from '~/types/api'

// Pairing card for one instance. It opens the pairing on mount (POST
// connect), renders the QR payload on a canvas, polls the connection status
// until the phone scan flips it to connected, and re-issues the QR
// automatically when qr_expires_at passes (GET qr replaces an expired code).
// A connected instance shows its linked state and never asks for a new
// pairing; the parent reloads the instance on the paired event.
const props = defineProps<{
  instanceId: string
  status: InstanceStatus
  whatsappJid: string
}>()

const emit = defineEmits<{
  paired: []
}>()

const { t } = useI18n()
const { connectInstance, getPairingQR, getConnectionStatus } = useInstances()

type Phase = 'starting' | 'pairing' | 'connected' | 'error'

const STATUS_POLL_MS = 3000
const REISSUE_RETRY_MS = 3000
const QR_SIZE = 224

const phase = ref<Phase>('starting')
const failure = ref<string | null>(null)
const reissueFailure = ref<string | null>(null)
const reissuing = ref(false)
const now = ref(Date.now())
const qrExpiresAt = ref<string | null>(null)

const qrHost = ref<HTMLDivElement | null>(null)
let qr: QRCodeStyling | null = null
let ticker: ReturnType<typeof setInterval> | null = null
let lastStatusPoll = 0
let retryAt = 0
let polling = false
let starting = false
let disposed = false

const secondsLeft = computed(() => {
  if (!qrExpiresAt.value) {
    return null
  }
  const expiresMs = new Date(qrExpiresAt.value).getTime()
  if (Number.isNaN(expiresMs)) {
    return null
  }
  return Math.max(0, Math.ceil((expiresMs - now.value) / 1000))
})

function stopTicker() {
  if (ticker !== null) {
    clearInterval(ticker)
    ticker = null
  }
}

function clearQR() {
  qr = null
  if (qrHost.value) {
    qrHost.value.innerHTML = ''
  }
}

// The canvas keeps its own white background so the code stays scannable in
// either color mode.
async function drawQR(data: string) {
  await nextTick()
  if (disposed || !qrHost.value) {
    return
  }
  if (!qr) {
    qr = new QRCodeStyling({
      width: QR_SIZE,
      height: QR_SIZE,
      type: 'canvas',
      data,
      margin: 4,
      dotsOptions: { color: '#111827', type: 'square' },
      backgroundOptions: { color: '#ffffff' }
    })
    qr.append(qrHost.value)
  } else {
    await qr.update({ data })
  }
}

function applyPairing(result: ConnectResult) {
  qrExpiresAt.value = result.qr_expires_at ?? null
  retryAt = 0
  reissueFailure.value = null
  phase.value = 'pairing'
  lastStatusPoll = Date.now()
  startTicker()
}

function markConnected(notifyParent: boolean) {
  stopTicker()
  reissuing.value = false
  phase.value = 'connected'
  if (notifyParent && !disposed) {
    emit('paired')
  }
}

async function start() {
  if (starting || disposed) {
    return
  }
  starting = true
  failure.value = null
  reissueFailure.value = null
  phase.value = 'starting'
  try {
    const result = await connectInstance(props.instanceId)
    if (disposed) {
      return
    }
    // No QR payload means there is nothing to scan: stored credentials or
    // an already-connected session.
    if (result.status === 'connected' || !result.qr_code) {
      markConnected(true)
      return
    }
    applyPairing(result)
    await drawQR(result.qr_code)
  } catch (error) {
    if (disposed) {
      return
    }
    stopTicker()
    phase.value = 'error'
    failure.value = error instanceof ApiError ? error.message : t('instances.pairing.failed')
  } finally {
    starting = false
  }
}

// Re-issue through GET qr: it hands out the current code and starts a fresh
// pairing when the displayed one expired. A 409 means the phone scan landed
// while the code was on screen, so the card flips to connected.
async function reissue() {
  if (reissuing.value || disposed) {
    return
  }
  reissuing.value = true
  reissueFailure.value = null
  try {
    const result = await getPairingQR(props.instanceId)
    if (disposed) {
      return
    }
    if (result.status === 'connected' || !result.qr_code) {
      markConnected(true)
      return
    }
    applyPairing(result)
    await drawQR(result.qr_code)
  } catch (error) {
    if (disposed) {
      return
    }
    if (error instanceof ApiError && error.status === 409) {
      markConnected(true)
      return
    }
    retryAt = Date.now() + REISSUE_RETRY_MS
    reissueFailure.value = error instanceof ApiError ? error.message : t('instances.pairing.reissueFailed')
  } finally {
    if (!disposed) {
      reissuing.value = false
    }
  }
}

async function pollStatus() {
  if (polling || disposed || phase.value !== 'pairing') {
    return
  }
  polling = true
  try {
    const current = await getConnectionStatus(props.instanceId)
    if (!disposed && phase.value === 'pairing' && current.status === 'connected') {
      markConnected(true)
    }
  } catch {
    // Transient poll failures keep the loop alive; the visible QR stays
    // valid until qr_expires_at, when reissue surfaces a real error.
  } finally {
    polling = false
    lastStatusPoll = Date.now()
  }
}

function startTicker() {
  stopTicker()
  ticker = setInterval(() => {
    now.value = Date.now()
    if (disposed || phase.value !== 'pairing') {
      return
    }
    if (now.value - lastStatusPoll >= STATUS_POLL_MS) {
      void pollStatus()
    }
    const raw = qrExpiresAt.value ? new Date(qrExpiresAt.value).getTime() : NaN
    if (!Number.isNaN(raw) && now.value >= raw && now.value >= retryAt) {
      void reissue()
    }
  }, 1000)
}

function reset() {
  stopTicker()
  clearQR()
  failure.value = null
  reissueFailure.value = null
  reissuing.value = false
  qrExpiresAt.value = null
  retryAt = 0
  lastStatusPoll = 0
}

// The parent owns the instance row: when it reports connected the card stops
// asking for a scan, and when a fresh disconnect lands the card pairs again
// on its own so the next QR is already on screen.
watch(() => props.status, (next) => {
  if (disposed) {
    return
  }
  if (next === 'connected' && phase.value !== 'connected') {
    markConnected(false)
  } else if (next !== 'connected' && phase.value === 'connected') {
    reset()
    void start()
  }
})

watch(() => props.instanceId, () => {
  if (disposed) {
    return
  }
  reset()
  if (props.status === 'connected') {
    phase.value = 'connected'
  } else {
    void start()
  }
})

onMounted(() => {
  if (props.status === 'connected') {
    phase.value = 'connected'
    return
  }
  void start()
})

onUnmounted(() => {
  disposed = true
  stopTicker()
  qr = null
})
</script>

<template>
  <UCard data-testid="pairing-card">
    <template #header>
      <h2 class="font-medium text-highlighted">
        {{ t('instances.pairing.cardTitle') }}
      </h2>
    </template>

    <div v-if="phase === 'starting'" class="flex flex-col items-center gap-3 py-4">
      <USkeleton class="h-56 w-56" />
      <p class="text-sm text-muted">
        {{ t('instances.pairing.starting') }}
      </p>
    </div>

    <div v-else-if="phase === 'pairing'" class="flex flex-col items-center gap-3 py-2">
      <div ref="qrHost" data-testid="pairing-qr" class="rounded-lg bg-white p-3" />
      <p class="text-center text-sm text-muted">
        {{ t('instances.pairing.scanHint') }}
      </p>
      <p v-if="secondsLeft !== null" data-testid="pairing-countdown" class="text-sm font-medium text-highlighted">
        {{ t('instances.pairing.expiresIn', { seconds: secondsLeft }) }}
      </p>
      <UAlert
        v-if="reissueFailure"
        color="warning"
        variant="subtle"
        :title="reissueFailure"
      />
      <UButton
        color="neutral"
        variant="soft"
        icon="i-lucide-refresh-cw"
        :loading="reissuing"
        :label="reissuing ? t('instances.pairing.refreshing') : t('instances.pairing.refresh')"
        @click="reissue"
      />
    </div>

    <div v-else-if="phase === 'connected'" class="flex flex-col gap-3">
      <UAlert
        color="success"
        variant="subtle"
        data-testid="pairing-connected"
        :title="t('instances.pairing.connectedTitle')"
        :description="whatsappJid ? t('instances.pairing.connectedBody', { jid: whatsappJid }) : t('instances.pairing.connectedBodyNoJid')"
      />
    </div>

    <div v-else class="flex flex-col gap-3">
      <UAlert
        color="error"
        variant="subtle"
        :title="failure ?? t('instances.pairing.failed')"
      />
      <div>
        <UButton
          icon="i-lucide-qr-code"
          :label="t('common.retry')"
          @click="start"
        />
      </div>
    </div>
  </UCard>
</template>
