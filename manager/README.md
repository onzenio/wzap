# wzap manager

Web console for the wzap service, served by Go under `/manager/`.
Point-in-time fork of the Nuxt dashboard template
(`nuxt-ui-templates/dashboard` at `c4e041c`), kept shell-only: sidebar,
dark mode and command palette stay, demo pages plus `server/api` mocks are
gone. No upstream tracking; later screens are built on this shell.

## Setup

```bash
pnpm install
```

## Development

The browser always talks to the same origin (session cookie, no CORS).
`nuxt dev` proxies the API paths to a local Go service:

```bash
pnpm dev
```

Open `http://localhost:3000/manager/` and sign in with a manager account
(see the service seed via `WZAP_ADMIN_EMAIL` / `WZAP_ADMIN_PASSWORD`).

## Production

```bash
pnpm build
pnpm preview
```

The static embed of `.output/public` under `/manager/` is wired by the Go
service (fallback SPA); this package only produces the bundle.

## Structure

- `app/pages/` screens (`login`, `overview`); instance screens plug in here.
- `app/layouts/` `default` (sidebar shell) and `auth` (login).
- `app/composables/` `useApi` (envelope client), `useAuth` (session +
  global/instance scope), `useNavigation` (scope-filtered nav), `useDashboard`
  (shortcuts).
- `app/middleware/auth.global.ts` session guard.
- `i18n/locales/` English strings (`@nuxtjs/i18n`, single locale).
