import { defineConfig } from '@playwright/test'

// The installation under test: an address, its self-made certificates, and its session key. The
// install test runs this inside its virtual machine (tools/install-test/inside.sh, PHASE=e2e).
export default defineConfig({
  testDir: './tests',
  timeout: 60_000,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: `https://${process.env.CREW_HOSTNAME}`,
    ignoreHTTPSErrors: true,
  },
})
