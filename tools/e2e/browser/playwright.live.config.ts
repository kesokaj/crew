import { defineConfig } from '@playwright/test'

// The live checks (live/): real runs on the operator's subscriptions, so they are never part of the
// browser tests (playwright.config.ts) and run only when asked for, one at a time:
//   npx playwright test -c playwright.live.config.ts
export default defineConfig({
  testDir: './live',
  timeout: 10 * 60_000,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: `https://${process.env.CREW_HOSTNAME}`,
    ignoreHTTPSErrors: true,
    viewport: { width: 1280, height: 900 },
  },
})
