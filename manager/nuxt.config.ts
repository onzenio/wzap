// https://nuxt.com/docs/api/configuration/nuxt-config
// Local Go address used by the dev proxy rules below.
const devProxyTarget = process.env.NUXT_DEV_PROXY_TARGET || 'http://127.0.0.1:8081'

export default defineNuxtConfig({
  modules: [
    '@nuxt/eslint',
    '@nuxt/ui',
    '@nuxtjs/i18n',
    '@vueuse/nuxt'
  ],

  // Authenticated console embedded as static output (task 6.6 embeds
  // .output/public under /manager/): no SSR, the browser talks to the Go
  // API on the same origin with the wzap_session cookie.
  ssr: false,

  devtools: {
    enabled: true
  },

  css: ['~/assets/css/main.css'],

  app: {
    baseURL: '/manager/'
  },

  runtimeConfig: {
    public: {
      // Same-origin API base. Empty means the page origin itself, which is
      // where the Go service listens (routes live at /). The dev proxy
      // below covers `nuxt dev`; override only for same-origin layouts.
      apiBaseUrl: ''
    }
  },

  routeRules: {
    // Dev-only convenience: forward API paths to the Go service so the
    // browser keeps same-origin semantics (session cookie, no CORS). The
    // static embed has no Nitro server, so these rules are inert there.
    // NUXT_DEV_PROXY_TARGET overrides the default local address.
    '/auth/**': { proxy: `${devProxyTarget}/auth/**` },
    '/instances/**': { proxy: `${devProxyTarget}/instances/**` },
    '/users/**': { proxy: `${devProxyTarget}/users/**` },
    '/media/**': { proxy: `${devProxyTarget}/media/**` }
  },

  i18n: {
    locales: [
      { code: 'en', file: 'en.json' }
    ],
    defaultLocale: 'en',
    langDir: 'locales',
    strategy: 'no_prefix'
  },

  compatibilityDate: '2026-06-30',

  eslint: {
    config: {
      stylistic: {
        commaDangle: 'never',
        braceStyle: '1tbs'
      }
    }
  }
})
