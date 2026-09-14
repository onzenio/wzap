<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import MessageStatusBadge from '~/components/instances/MessageStatusBadge.vue'
import type { InstanceStatus, MediaKind, NumberCheckResult, OutboundMessage } from '~/types/api'

// Test-send card for one instance: number check (numbers/check, which never
// enqueues), text send and multipart media send (202 with a queued id), plus
// polling of the accepted message until the terminal sent/failed state. Sends
// require a connected instance; anything else answers 409 without enqueueing
// and is surfaced as a friendly message. An unknown number answers 422
// without enqueueing, surfaced verbatim from the server message.
const props = defineProps<{
  instanceId: string
  status: InstanceStatus
}>()

const emit = defineEmits<{
  sent: [messageId: string]
  settled: [messageId: string]
}>()

const { t } = useI18n()
const toast = useToast()
const { checkNumber, sendText, sendMedia, getMessage } = useMessages()

const TRACK_POLL_MS = 2000
const TRACK_MAX_POLLS = 30

const canSend = computed(() => props.status === 'connected')

const checkPhone = ref('')
const checking = ref(false)
const checkResult = ref<NumberCheckResult | null>(null)
const checkFailure = ref<string | null>(null)

const textTo = ref('')
const textBody = ref('')
const sendingText = ref(false)
const sendFailure = ref<string | null>(null)

const mediaTo = ref('')
const mediaCaption = ref('')
const mediaFilename = ref('')
const mediaPtt = ref(false)
const sendingMedia = ref(false)
const mediaFailure = ref<string | null>(null)
const mediaUnsupported = ref<string | null>(null)
const mediaKind = ref<MediaKind | null>(null)
const mediaName = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

const acceptedId = ref<string | null>(null)
const tracked = ref<OutboundMessage | null>(null)
const tracking = ref(false)
const trackTimedOut = ref(false)
const trackFailure = ref<string | null>(null)

let tracker: ReturnType<typeof setInterval> | null = null
let polls = 0
let trackingToken = 0

function stopTracker() {
  if (tracker !== null) {
    clearInterval(tracker)
    tracker = null
  }
}

function reset() {
  stopTracker()
  trackingToken += 1
  checkPhone.value = ''
  checkResult.value = null
  checkFailure.value = null
  textTo.value = ''
  textBody.value = ''
  sendFailure.value = null
  mediaTo.value = ''
  mediaCaption.value = ''
  mediaFilename.value = ''
  mediaPtt.value = false
  mediaFailure.value = null
  mediaUnsupported.value = null
  mediaKind.value = null
  mediaName.value = ''
  if (fileInput.value) {
    fileInput.value.value = ''
  }
  acceptedId.value = null
  tracked.value = null
  tracking.value = false
  trackTimedOut.value = false
  trackFailure.value = null
}

async function onCheck() {
  if (checking.value) {
    return
  }
  const phone = checkPhone.value.trim()
  if (phone === '') {
    checkFailure.value = t('instances.send.phoneRequired')
    return
  }
  checking.value = true
  checkFailure.value = null
  checkResult.value = null
  try {
    checkResult.value = await checkNumber(props.instanceId, phone)
  } catch (error) {
    checkFailure.value = friendlyCheckError(error)
  } finally {
    checking.value = false
  }
}

function friendlyCheckError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 503) {
      return t('instances.send.resolutionUnavailable')
    }
    return error.message
  }
  return t('instances.send.checkFailed')
}

async function onSendText() {
  if (sendingText.value) {
    return
  }
  const to = textTo.value.trim()
  if (to === '') {
    sendFailure.value = t('instances.send.phoneRequired')
    return
  }
  if (textBody.value.trim() === '') {
    sendFailure.value = t('instances.send.textRequired')
    return
  }
  sendingText.value = true
  sendFailure.value = null
  try {
    const accepted = await sendText(props.instanceId, to, textBody.value)
    toast.add({ title: t('instances.send.sentToast'), color: 'success' })
    emit('sent', accepted.message_id)
    void track(accepted.message_id)
  } catch (error) {
    sendFailure.value = friendlySendError(error)
  } finally {
    sendingText.value = false
  }
}

function onFileChange(event: Event) {
  mediaUnsupported.value = null
  mediaKind.value = null
  mediaName.value = ''
  const target = event.target as HTMLInputElement | null
  const file = target?.files?.[0] ?? null
  if (!file) {
    return
  }
  const kind = kindForFile(file)
  if (!kind) {
    mediaUnsupported.value = t('instances.send.unsupportedFile')
    return
  }
  mediaKind.value = kind
  mediaName.value = file.name
  if (kind !== 'audio') {
    mediaPtt.value = false
  }
}

async function onSendMedia() {
  if (sendingMedia.value) {
    return
  }
  const to = mediaTo.value.trim()
  if (to === '') {
    mediaFailure.value = t('instances.send.phoneRequired')
    return
  }
  const file = fileInput.value?.files?.[0] ?? null
  if (!file) {
    mediaFailure.value = t('instances.send.fileRequired')
    return
  }
  const kind = kindForFile(file)
  if (!kind) {
    mediaFailure.value = t('instances.send.unsupportedFile')
    return
  }
  sendingMedia.value = true
  mediaFailure.value = null
  try {
    const accepted = await sendMedia(props.instanceId, {
      to,
      type: kind,
      caption: mediaCaption.value,
      filename: mediaFilename.value.trim(),
      ptt: kind === 'audio' ? mediaPtt.value : false,
      file
    })
    toast.add({ title: t('instances.send.sentToast'), color: 'success' })
    emit('sent', accepted.message_id)
    void track(accepted.message_id)
  } catch (error) {
    mediaFailure.value = friendlySendError(error)
  } finally {
    sendingMedia.value = false
  }
}

function friendlySendError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return t('instances.send.notConnected')
    }
    if (error.status === 503) {
      return t('instances.send.resolutionUnavailable')
    }
    return error.message
  }
  return t('instances.send.failed')
}

// Track one accepted message until sent/failed, polling the message detail.
// A new send supersedes the previous track via the token guard.
async function track(messageId: string) {
  const token = trackingToken + 1
  trackingToken = token
  stopTracker()
  polls = 0
  acceptedId.value = messageId
  tracked.value = null
  tracking.value = true
  trackTimedOut.value = false
  trackFailure.value = null
  const first = await pollTracked(messageId, token)
  if (tracking.value && isTokenCurrent(token) && !isTerminal(first?.status)) {
    tracker = setInterval(() => {
      void pollTracked(messageId, token)
    }, TRACK_POLL_MS)
  }
}

// pollTracked fetches the tracked message once and advances the terminal
// state machine. It returns the fetched message (null when superseded or
// failed) so track() above does not read tracked.value under its null
// narrowing, which would resolve the optional chain against never.
async function pollTracked(messageId: string, token: number): Promise<OutboundMessage | null> {
  if (!isTokenCurrent(token)) {
    return null
  }
  polls += 1
  try {
    const current = await getMessage(props.instanceId, messageId)
    if (!isTokenCurrent(token)) {
      return null
    }
    tracked.value = current
    if (isTerminal(current.status)) {
      tracking.value = false
      stopTracker()
      emit('settled', messageId)
    } else if (polls >= TRACK_MAX_POLLS) {
      tracking.value = false
      trackTimedOut.value = true
      stopTracker()
    }
    return current
  } catch (error) {
    if (!isTokenCurrent(token)) {
      return null
    }
    tracking.value = false
    stopTracker()
    trackFailure.value = error instanceof ApiError ? error.message : t('instances.send.trackFailed')
    return null
  }
}

function isTokenCurrent(token: number): boolean {
  return token === trackingToken
}

function isTerminal(status: string | undefined): boolean {
  return status === 'sent' || status === 'failed'
}

// Same mapping as media.Kind on the Go side: every image goes as image,
// every video as video, every audio as audio and the remaining allowed
// formats go as documents. Unknown MIME means the upload would answer 422,
// so it is rejected before anything is sent.
function kindForFile(file: File): MediaKind | null {
  const mime = (file.type || mimeForExtension(file.name)).toLowerCase().trim()
  if (mime === '' || !ALLOWED_MIMES.has(mime)) {
    return null
  }
  if (mime.startsWith('image/')) {
    return 'image'
  }
  if (mime.startsWith('video/')) {
    return 'video'
  }
  if (mime.startsWith('audio/')) {
    return 'audio'
  }
  return 'document'
}

// Mirrors allowedMimes in internal/media/storage.go so unsupported files are
// rejected client-side with the same verdict the server would return.
const ALLOWED_MIMES: ReadonlySet<string> = new Set([
  'image/jpeg',
  'image/png',
  'image/webp',
  'video/mp4',
  'video/3gpp',
  'audio/aac',
  'audio/amr',
  'audio/mpeg',
  'audio/mp4',
  'audio/ogg',
  'application/pdf',
  'text/plain',
  'application/msword',
  'application/vnd.ms-excel',
  'application/vnd.ms-powerpoint',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation'
])

// Browsers leave file.type empty for unknown extensions; map the common ones
// so those uploads still declare their matching type.
function mimeForExtension(name: string): string {
  const dot = name.lastIndexOf('.')
  const extension = dot >= 0 ? name.slice(dot + 1).toLowerCase() : ''
  switch (extension) {
    case 'jpg':
    case 'jpeg':
      return 'image/jpeg'
    case 'png':
      return 'image/png'
    case 'webp':
      return 'image/webp'
    case 'mp4':
    case 'm4v':
      return 'video/mp4'
    case '3gp':
    case '3gpp':
      return 'video/3gpp'
    case 'aac':
      return 'audio/aac'
    case 'amr':
      return 'audio/amr'
    case 'mp3':
      return 'audio/mpeg'
    case 'm4a':
      return 'audio/mp4'
    case 'ogg':
    case 'oga':
      return 'audio/ogg'
    case 'pdf':
      return 'application/pdf'
    case 'txt':
      return 'text/plain'
    case 'doc':
      return 'application/msword'
    case 'xls':
      return 'application/vnd.ms-excel'
    case 'ppt':
      return 'application/vnd.ms-powerpoint'
    case 'docx':
      return 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
    case 'xlsx':
      return 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
    case 'pptx':
      return 'application/vnd.openxmlformats-officedocument.presentationml.presentation'
    default:
      return ''
  }
}

watch(() => props.instanceId, () => {
  reset()
})

onUnmounted(() => {
  trackingToken += 1
  stopTracker()
})
</script>

<template>
  <UCard data-testid="test-send-card">
    <template #header>
      <h2 class="font-medium text-highlighted">
        {{ t('instances.send.cardTitle') }}
      </h2>
    </template>

    <div class="flex flex-col gap-6">
      <UAlert
        v-if="!canSend"
        color="warning"
        variant="subtle"
        :title="t('instances.send.notConnected')"
      />

      <form class="flex flex-col gap-3" @submit.prevent="onCheck">
        <h3 class="text-sm font-medium text-highlighted">
          {{ t('instances.send.numberTitle') }}
        </h3>
        <UAlert
          v-if="checkFailure"
          color="error"
          variant="subtle"
          :title="checkFailure"
        />
        <UAlert
          v-if="checkResult && checkResult.exists"
          color="success"
          variant="subtle"
          :title="t('instances.send.numberFound')"
          :description="checkResult.jid"
        />
        <UAlert
          v-if="checkResult && !checkResult.exists"
          color="warning"
          variant="subtle"
          :title="t('instances.send.numberMissing')"
        />
        <div class="flex flex-col gap-2 sm:flex-row">
          <UInput
            v-model="checkPhone"
            maxlength="64"
            :placeholder="t('instances.send.phonePlaceholder')"
            class="w-full font-mono"
          />
          <UButton
            type="submit"
            color="neutral"
            variant="soft"
            icon="i-lucide-phone-search"
            :loading="checking"
            :label="checking ? t('instances.send.checking') : t('instances.send.check')"
          />
        </div>
      </form>

      <form class="flex flex-col gap-3" @submit.prevent="onSendText">
        <h3 class="text-sm font-medium text-highlighted">
          {{ t('instances.send.textTitle') }}
        </h3>
        <UAlert
          v-if="sendFailure"
          color="error"
          variant="subtle"
          :title="sendFailure"
        />
        <UFormField :label="t('instances.send.to')" name="text_to" required>
          <UInput
            v-model="textTo"
            required
            maxlength="64"
            :placeholder="t('instances.send.phonePlaceholder')"
            class="w-full font-mono"
          />
        </UFormField>
        <UFormField :label="t('instances.send.text')" name="text_body" required>
          <UTextarea
            v-model="textBody"
            required
            maxlength="4096"
            :rows="3"
            class="w-full"
          />
        </UFormField>
        <div class="flex justify-end">
          <UButton
            type="submit"
            icon="i-lucide-send"
            :disabled="!canSend"
            :loading="sendingText"
            :label="sendingText ? t('instances.send.sending') : t('instances.send.send')"
          />
        </div>
      </form>

      <form class="flex flex-col gap-3" @submit.prevent="onSendMedia">
        <h3 class="text-sm font-medium text-highlighted">
          {{ t('instances.send.mediaTitle') }}
        </h3>
        <UAlert
          v-if="mediaFailure"
          color="error"
          variant="subtle"
          :title="mediaFailure"
        />
        <UAlert
          v-if="mediaUnsupported"
          color="error"
          variant="subtle"
          :title="mediaUnsupported"
        />
        <UFormField :label="t('instances.send.to')" name="media_to" required>
          <UInput
            v-model="mediaTo"
            required
            maxlength="64"
            :placeholder="t('instances.send.phonePlaceholder')"
            class="w-full font-mono"
          />
        </UFormField>
        <UFormField
          :label="t('instances.send.file')"
          :hint="t('instances.send.fileHint')"
          name="media_file"
          required
        >
          <input
            ref="fileInput"
            type="file"
            required
            class="w-full text-sm text-highlighted file:mr-3 file:rounded-md file:border-0 file:bg-primary file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-white"
            @change="onFileChange"
          >
        </UFormField>
        <p v-if="mediaKind" class="font-mono text-sm text-muted">
          {{ mediaName }} — {{ t('instances.send.detectedKind', { kind: mediaKind }) }}
        </p>
        <UFormField :label="t('instances.send.caption')" name="media_caption">
          <UInput v-model="mediaCaption" maxlength="1024" class="w-full" />
        </UFormField>
        <UFormField :label="t('instances.send.filename')" :hint="t('instances.send.filenameHint')" name="media_filename">
          <UInput v-model="mediaFilename" maxlength="255" class="w-full font-mono" />
        </UFormField>
        <UCheckbox
          v-if="mediaKind === 'audio'"
          v-model="mediaPtt"
          :label="t('instances.send.ptt')"
        />
        <div class="flex justify-end">
          <UButton
            type="submit"
            icon="i-lucide-paperclip"
            :disabled="!canSend"
            :loading="sendingMedia"
            :label="sendingMedia ? t('instances.send.sending') : t('instances.send.sendMedia')"
          />
        </div>
      </form>

      <div v-if="acceptedId" class="flex flex-col gap-3 border-t border-default pt-4">
        <h3 class="text-sm font-medium text-highlighted">
          {{ t('instances.send.trackingTitle') }}
        </h3>
        <p v-if="tracking" class="flex items-center gap-2 text-sm text-muted">
          <UIcon name="i-lucide-loader-circle" class="animate-spin" />
          {{ t('instances.send.tracking') }}
        </p>
        <UAlert
          v-if="trackFailure"
          color="error"
          variant="subtle"
          :title="trackFailure"
        />
        <UAlert
          v-if="trackTimedOut"
          color="warning"
          variant="subtle"
          :title="t('instances.send.settleTimeout')"
        />
        <dl v-if="tracked" class="flex flex-col gap-2 text-sm">
          <div class="flex flex-wrap items-center gap-2">
            <MessageStatusBadge :status="tracked.status" />
            <span class="font-mono text-xs text-muted">{{ tracked.id }}</span>
          </div>
          <div v-if="tracked.last_error" class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.lastError') }}
            </dt>
            <dd class="text-right text-highlighted">
              {{ tracked.last_error }}
            </dd>
          </div>
          <div v-if="tracked.whatsapp_message_id" class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.whatsappId') }}
            </dt>
            <dd class="font-mono text-highlighted">
              {{ tracked.whatsapp_message_id }}
            </dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.attempts') }}
            </dt>
            <dd class="text-highlighted">
              {{ tracked.attempts }}
            </dd>
          </div>
        </dl>
      </div>
    </div>
  </UCard>
</template>
