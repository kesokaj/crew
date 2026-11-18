import { createHash, createHmac } from 'node:crypto'

// A session token as the ingress's sign-in writes one, signed with the installation's own key
// (/etc/crew/secrets/jwt_key). Only a test machine's key is ever read for this.
const host = process.env.CREW_HOSTNAME ?? ''
const key = process.env.CREW_JWT_KEY ?? ''

// The cookie's name is the deployment's prefix (ingress/config.go, CookiePrefix).
export const cookieName =
  'AUTHP' + createHash('sha256').update(host.toLowerCase()).digest('hex').slice(0, 8).toUpperCase() + '_ACCESS_TOKEN'

const b64 = (v: object | Buffer) => (Buffer.isBuffer(v) ? v : Buffer.from(JSON.stringify(v))).toString('base64url')

export function token(opts: { email: string; roles: string[]; lifetime?: number; key?: string }): string {
  const now = Math.floor(Date.now() / 1000)
  const header = { alg: 'HS512', typ: 'JWT' }
  const claims = {
    sub: opts.email,
    email: opts.email,
    name: 'Test',
    roles: opts.roles,
    iss: `https://auth.${host}/oauth2/google/`,
    iat: now,
    nbf: now - 60,
    exp: now + (opts.lifetime ?? 3600),
  }
  const body = `${b64(header)}.${b64(claims)}`
  const sig = createHmac('sha512', opts.key ?? key).update(body).digest()
  return `${body}.${b64(sig)}`
}

export function cookie(value: string) {
  return { name: cookieName, value, domain: host, path: '/', secure: true, httpOnly: true }
}

export const admin = process.env.CREW_ADMIN_EMAIL ?? ''
export const adminRole = 'app/crew/admin'
