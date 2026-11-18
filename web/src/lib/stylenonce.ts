// The shell's policy lets in style elements only with the nonce of its scripts (internal/httpapi,
// serveShell). The terminal (xterm.js) makes style elements of its own as it starts; this puts the
// page's nonce on every style element the page's code makes, so they are let in, and nothing else is.
let installed = false

export function installStyleNonce() {
  if (installed || typeof document === 'undefined') return
  const nonce = document.querySelector<HTMLScriptElement>('script[nonce]')?.nonce
  if (!nonce) return
  installed = true
  const create = document.createElement.bind(document)
  document.createElement = ((tag: string, options?: ElementCreationOptions) => {
    const el = create(tag, options)
    if (tag.toLowerCase() === 'style') el.setAttribute('nonce', nonce)
    return el
  }) as typeof document.createElement
}
