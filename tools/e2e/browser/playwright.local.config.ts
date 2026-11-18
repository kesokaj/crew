import { defineConfig } from '@playwright/test'

// The browser tests of a throwaway local installation (plan M17, D28): a server built from the checkout,
// with its own database, behind a Caddy proxy that injects ingress headers for crew.test with a local
// certificate and secret (for example, a throwaway stack wrapper under gitignored .agents/scratch/).
// Only the specs that need no real sign-in are named here; the stack sets CREW_E2E_WRITES so they may change settings.
export default defineConfig({
  testDir: './tests',
  testMatch: /m(1[789]|20)\.spec\.ts$/,
  outputDir: '/tmp/local-results',
  timeout: 60_000,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.CREW_CHECK_URL,
    ignoreHTTPSErrors: true,
  },
})
