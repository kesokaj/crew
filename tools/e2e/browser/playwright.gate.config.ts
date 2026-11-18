import { readFileSync } from 'node:fs'
import { defineConfig } from '@playwright/test'

// The gate's browser tests (design 16.2, 0.48): the screens of a release in check mode, beside the
// running one, on its scratch copy of the database. They only read. There is no ingress in front of
// the check server, so every request carries what the ingress would add: the check's own secret,
// made for this check, which no other server takes, and the admin email. The updater runs them in a microVM on
// the check's network, where nothing else answers.
const secret = process.env.CREW_INGRESS_TOKEN_FILE ? readFileSync(process.env.CREW_INGRESS_TOKEN_FILE, 'utf8').trim() : ''

export default defineConfig({
  testDir: './gate',
  // The tests' folder is read only in the microVM, and Playwright removes its results' folder as it
  // starts: the results go outside it (the gate of 0.2.14 on dave). The updater names its own with
  // --output, inside a tmpfs; this is for an updater that does not.
  outputDir: '/tmp/gate-results',
  timeout: 60_000,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.CREW_CHECK_URL ?? 'http://crew-server-check:8080',
    extraHTTPHeaders: {
      'X-Crew-Ingress': secret,
      'X-Caddy-Email': process.env.CREW_ADMIN_EMAIL ?? '',
    },
  },
})
