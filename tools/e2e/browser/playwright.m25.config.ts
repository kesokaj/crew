import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  testMatch: /(m25|m26|screens|nodollars)\.spec\.ts$/,
  outputDir: '/tmp/m25-results',
  timeout: 120_000,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.CREW_M25_URL || 'http://127.0.0.1:18080',
    ignoreHTTPSErrors: true,
  },
})
