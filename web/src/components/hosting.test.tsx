import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { HostingView } from '../lib/api'
import { Cards, logLine } from '../routes/p.$project.hosting'
import { when } from './ui'

const baseView: HostingView = {
  state: 'live',
  address: 'https://habit-tracker.apps.example.com',
  certificate: '*.apps.example.com',
  access: 'private',
  allowed: [],
  followMain: false,
  internet: true,
  dockerfile: 'Dockerfile',
  port: 8080,
  health: '/healthz',
  data: '/data',
  size: 'small',
  sizeText: '1 CPU, 1 GB',
  logDays: 14,
  keepImages: 3,
  compose: false,
  values: [],
  valuesLost: false,
  deploys: [],
  running: 0,
}

describe('Hosting logLine', () => {
  it('formats lines that begin with an RFC 3339 / ISO-8601 timestamp', () => {
    const stamp = '2026-11-07T17:12:00Z'
    const text = 'crew: deploy 1 failed: the build failed: the build ended with 1'
    expect(logLine(`${stamp} ${text}`)).toBe(`${when(stamp)}  ${text}`)

    const nano = '2026-11-07T17:12:00.123456789+01:00'
    expect(logLine(`${nano} GET /healthz 200`)).toBe(`${when(nano)}  GET /healthz 200`)
  })

  it('leaves Docker BuildKit #0, #1, #2 step prefixes and non-ISO tokens untouched', () => {
    const lines = [
      '#0 building with "default" instance using docker driver',
      '#1 [internal] load build definition from Dockerfile',
      '#2 [internal] load metadata for docker.io/library/python:3.13-slim',
      '2001 plain number prefix that V8 Date.parse accepts',
      'plain continuation line without a timestamp',
    ]
    for (const l of lines) {
      expect(logLine(l)).toBe(l)
    }
  })
})

describe('Hosting Cards Sign-in callback URI', () => {
  it('shows the apps portal Google OAuth callback URI when access is not public', () => {
    for (const access of ['private', 'listed', 'google']) {
      const html = renderToStaticMarkup(<Cards h={{ ...baseView, access }} />)
      expect(html).toContain('https://auth.apps.example.com/oauth2/google/authorization-code-callback')
    }
  })

  it('omits the callback URI when access is public or no certificate domain is present', () => {
    const pub = renderToStaticMarkup(<Cards h={{ ...baseView, access: 'public' }} />)
    expect(pub).not.toContain('/oauth2/google/authorization-code-callback')

    const refused = renderToStaticMarkup(<Cards h={{ ...baseView, certificate: undefined }} />)
    expect(refused).not.toContain('/oauth2/google/authorization-code-callback')
  })
})
