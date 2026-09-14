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

  app: {
    baseURL: '/manager/'
  },

  css: ['~/assets/css/main.css'],

  runtimeConfig: {
    public: {
      // Same-origin API base. Empty means the page origin itself, which is
      // where the Go service listens (routes live at /). The dev proxy
      // below covers `nuxt dev`; override only for same-origin layouts.
      apiBaseUrl: ''
    }
  },

  routeRules: {
    // No dev proxy here on purpose: Nitro matches route rules on the path
    // with app.baseURL stripped, so a rule like /instances/** would also
    // catch the /manager/instances page and answer 400 (out-of-scope proxy
    // base). The static embed (task 6.6) has no Nitro server, so any rule
    // left here would only affect dev and preview. The Vite dev proxy below
    // matches the raw path instead and only fires at the origin root, where
    // the browser API client calls.
  },

  compatibilityDate: '2026-06-30',

  vite: {
    server: {
      // Dev-only convenience: forward origin-root API paths to the Go
      // service so the browser keeps same-origin semantics (session cookie,
      // no CORS). Page routes live under /manager/ and never match these
      // prefixes. NUXT_DEV_PROXY_TARGET overrides the default local address.
      proxy: {
        '/auth': devProxyTarget,
        '/instances': devProxyTarget,
        '/users': devProxyTarget,
        '/media': devProxyTarget
      }
    }
  },

  eslint: {
    config: {
      stylistic: {
        commaDangle: 'never',
        braceStyle: '1tbs'
      }
    }
  },

  i18n: {
    locales: [
      { code: 'en', file: 'en.json' }
    ],
    defaultLocale: 'en',
    langDir: 'locales',
    strategy: 'no_prefix'
  }
})
