// trailers are the lines of a commit message's last paragraph when every one of them reads
// "Key: value", as git's own trailers do.
export function trailers(body: string): string {
  const last = body.trim().split(/\n\s*\n/).at(-1) ?? ''
  const lines = last.split('\n').filter((l) => /^[A-Za-z][A-Za-z-]*: \S/.test(l))
  return lines.length && lines.length === last.split('\n').length ? lines.join('\n') : ''
}
