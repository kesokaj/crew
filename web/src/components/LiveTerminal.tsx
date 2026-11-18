import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { installStyleNonce } from '../lib/stylenonce'

// LiveTerminal is a terminal in the browser on a tmux session in a microVM (13.11): binary frames
// carry the terminal's bytes both ways, text frames {"type":"resize"} and {"type":"ping"}. The
// session lives on when the page goes, and a page that comes back finds it as it was. Close code
// 4000 says the shell ended, 4001 that another page took the terminal.
export function LiveTerminal({ path, onEnded }: { path: string; onEnded?: (why: string) => void }) {
  const box = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState('Connecting…')
  const [line, setLine] = useState('')
  const sendLine = useRef<(s: string) => void>(() => {})
  const ended = useRef(onEnded)
  ended.current = onEnded

  useEffect(() => {
    installStyleNonce()
    const el = box.current
    if (!el) return
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      theme: { background: '#121417', foreground: '#e8eaed' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
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
    term.open(el)
    fit.fit()
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const sep = path.includes('?') ? '&' : '?'
    const ws = new WebSocket(`${proto}://${location.host}${path}${sep}cols=${term.cols}&rows=${term.rows}`)
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => setStatus('')
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data))
    }
    ws.onclose = (ev) => {
      const why =
        ev.code === 4000 ? 'The shell ended.' : ev.code === 4001 ? 'Another page took this terminal.' : 'The connection ended. Reload to attach again.'
      setStatus(why)
      ended.current?.(why)
    }
    const send = (d: string) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d))
    }
    const input = term.onData(send)
    sendLine.current = (s: string) => {
      send(s + '\r')
      term.focus()
    }
    const sendSize = () => {
      fit.fit()
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
    const ro = new ResizeObserver(sendSize)
    ro.observe(el)
    const ping = setInterval(() => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'ping' }))
    }, 25000)
    term.focus()
    return () => {
      clearInterval(ping)
      ro.disconnect()
      ws.onopen = ws.onmessage = ws.onclose = null
      input.dispose()
      ws.close()
      term.dispose()
      sendLine.current = () => {}
    }
  }, [path])

  return (
    <div>
      {status && <p className="mb-2 text-sm text-text-dim" data-terminal-status>{status}</p>}
      <div ref={box} data-terminal className="h-[60vh] min-h-80 w-full overflow-hidden rounded-md bg-term p-2" />
      <form
        className="mt-2 flex gap-2 text-sm"
        onSubmit={(e) => {
          e.preventDefault()
          sendLine.current(line)
          setLine('')
        }}
      >
        <input
          aria-label="Type a command"
          className="min-w-0 flex-1 rounded-md border border-border bg-bg-raised px-2 py-1 font-mono"
          placeholder="Type a command"
          autoComplete="off"
          spellCheck={false}
          value={line}
          onChange={(e) => setLine(e.target.value)}
        />
        <button className="rounded-md border border-border bg-bg-raised px-3 py-1">Run</button>
      </form>
    </div>
  )
}
