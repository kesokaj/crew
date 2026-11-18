import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { installStyleNonce } from '../lib/stylenonce'
import { findSpans, type Row } from '../lib/termlinks'

export type StepResult = { step: string; ok: boolean; message: string }

// A terminal of Setup: one step of crew-setup in the setup microVM (design 12.6). Binary frames carry
// the terminal's bytes; text frames carry {"type":"status"|"done"} from the server and
// {"type":"resize"} to it. The step ends when crew-setup ends; the server then collects what it
// made and removes the microVM. The terminal stays on the page after that, so what the step printed
// last can still be read.
export function SetupTerminal({ step, onDone }: { step: string; onDone: (r: StepResult) => void }) {
  const box = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState('Connecting…')
  const [line, setLine] = useState('')
  // The address a link under the pointer opens: a link's text and its target may differ.
  const [hover, setHover] = useState('')
  const sendLine = useRef<(s: string) => void>(() => {})
  const done = useRef(onDone)
  done.current = onDone

  useEffect(() => {
    installStyleNonce()
    const el = box.current
    if (!el) return
    const term = new XTerm({
      cursorBlink: true,
      convertEol: false,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      theme: { background: '#1e1f20', foreground: '#e8eaed' },
      // A link a CLI marks as one (OSC 8) opens in a new tab, as the links found below do.
      linkHandler: { activate: (_e, uri) => openLink(uri), hover: (_e, uri) => setHover(uri), leave: () => setHover('') },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    // Ctrl+V (and Cmd+V) is the browser's paste, which the terminal then takes as typed text; the
    // terminal would otherwise send it on as the control key. Ctrl+C copies when text is selected,
    // and is Ctrl+C to the step when nothing is.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown' || !(e.ctrlKey || e.metaKey)) return true
      const k = e.key.toLowerCase()
      if (k === 'v') return false
      if (k === 'c' && term.hasSelection()) {
        void navigator.clipboard?.writeText(term.getSelection()).catch(() => {})
        return false
      }
      return true
    })
    // Links are clickable where they are printed, also one that runs over several rows.
    const links = term.registerLinkProvider({
      provideLinks(line, give) {
        const buf = term.buffer.active
        const from = Math.max(0, line - 1 - 40)
        const to = Math.min(buf.length, line + 40)
        const rows: Row[] = []
        for (let i = from; i < to; i++) {
          const l = buf.getLine(i)
          rows.push({ text: l?.translateToString(false) ?? '', wrapped: !!l?.isWrapped })
        }
        const y = line - 1 - from
        const found = findSpans(rows).filter((s) => s.start.y <= y && s.end.y >= y)
        give(
          found.map((s) => ({
            text: s.url,
            range: { start: { x: s.start.x + 1, y: s.start.y + from + 1 }, end: { x: s.end.x + 1, y: s.end.y + from + 1 } },
            activate: () => openLink(s.url),
            hover: () => setHover(s.url),
            leave: () => setHover(''),
          })),
        )
      },
    })
    term.open(el)
    fit.fit()
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(
      `${proto}://${location.host}/api/setup/terminal?step=${encodeURIComponent(step)}&cols=${term.cols}&rows=${term.rows}`,
    )
    ws.binaryType = 'arraybuffer'
    let finished = false
    let printed = false
    ws.onopen = () => setStatus('')
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        if (!printed) {
          printed = true
          setStatus('')
        }
        term.write(new Uint8Array(ev.data))
        return
      }
      try {
        const m = JSON.parse(ev.data as string) as { type: string; text?: string } & StepResult
        if (m.type === 'status') setStatus(m.text ?? '')
        if (m.type === 'done') {
          finished = true
          setStatus('')
          done.current({ step: m.step, ok: m.ok, message: m.message })
        }
      } catch {
        // A text frame that is not ours says nothing.
      }
    }
    ws.onclose = () => {
      if (!finished) {
        setStatus('The connection ended before the step did. Nothing was collected.')
        done.current({ step, ok: false, message: 'the connection ended' })
      }
    }
    const toStep = (d: string) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d))
    }
    const input = term.onData(toStep)
    sendLine.current = (s: string) => {
      toStep(s + '\r')
      term.focus()
    }
    const sendSize = () => {
      fit.fit()
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
    const ro = new ResizeObserver(sendSize)
    ro.observe(el)
    term.focus()
    return () => {
      ro.disconnect()
      // A terminal that is closed says nothing more: a late close must not end the next step.
      ws.onopen = ws.onmessage = ws.onclose = null
      links.dispose()
      input.dispose()
      ws.close()
      term.dispose()
      sendLine.current = () => {}
    }
  }, [step])

  return (
    <div>
      {status && <p className="mb-2 text-sm text-text-dim">{status}</p>}
      <div ref={box} className="h-[70vh] min-h-96 w-full overflow-hidden rounded-md bg-[#1e1f20] p-2" />
      <p className="mt-1 break-all text-xs text-text-dim">{hover ? `Opens ${hover}` : 'Click a link in the terminal to open it in a new tab.'}</p>
      <form
        className="mt-2 flex gap-2 text-sm"
        onSubmit={(e) => {
          e.preventDefault()
          sendLine.current(line)
          setLine('')
        }}
      >
        <input
          aria-label="A line for the terminal"
          className="min-w-0 flex-1 rounded-md border border-border bg-bg px-2 py-1"
          placeholder="Paste a code here, then Send. Ctrl+V in the terminal works too."
          autoComplete="off"
          spellCheck={false}
          value={line}
          onChange={(e) => setLine(e.target.value)}
        />
        <button className="rounded-md border border-border px-3 py-1">Send</button>
      </form>
    </div>
  )
}

// Only an https link is opened, in a tab of its own that cannot reach back to this page.
function openLink(uri: string) {
  if (uri.startsWith('https://')) window.open(uri, '_blank', 'noopener,noreferrer')
}
