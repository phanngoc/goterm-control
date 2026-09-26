import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'

// TerminalPanel is the editor's bottom panel: shells in the project folder,
// one per tab, on a real PTY served by the dashboard process (/api/term).
//
// Tabs stay mounted while the panel is hidden, so hiding it with Ctrl+` does
// not kill what is running — only closing a tab (or the editor) does, and the
// server then hangs up everything that shell started.

interface Tab {
  id: number
  title: string
  gen: number // bumped to reconnect a finished session in place
}

export default function TerminalPanel({ channelID, visible, onHide, maximized, onToggleMax }: {
  channelID: string
  visible: boolean
  onHide: () => void
  maximized: boolean
  onToggleMax: () => void
}) {
  const [tabs, setTabs] = useState<Tab[]>([{ id: 1, title: 'shell', gen: 0 }])
  const [active, setActive] = useState(1)
  const [ended, setEnded] = useState<Record<number, string>>({})
  const nextID = useRef(2)

  const add = () => {
    const id = nextID.current++
    setTabs(ts => [...ts, { id, title: 'shell', gen: 0 }])
    setActive(id)
  }
  const close = (id: number) => {
    const rest = tabs.filter(t => t.id !== id)
    setTabs(rest)
    if (rest.length === 0) {
      // The last tab closing hides the panel. The next shell starts when the
      // panel is opened again, not now in the background.
      onHide()
      return
    }
    if (active === id) setActive(rest[rest.length - 1].id)
  }

  useEffect(() => {
    if (visible && tabs.length === 0) add()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, tabs.length])
  const restart = (id: number) => {
    setEnded(e => { const n = { ...e }; delete n[id]; return n })
    setTabs(ts => ts.map(t => t.id === id ? { ...t, gen: t.gen + 1, title: 'shell' } : t))
  }

  return (
    <div className="h-full flex flex-col bg-[#1e1e1e] border-t border-black/60">
      <div className="flex items-center h-8 px-2 gap-1 text-[12px] bg-[#181818] shrink-0 overflow-x-auto">
        <span className="px-1 text-[11px] uppercase tracking-wide text-gray-500">Terminal</span>
        {tabs.map(t => (
          <div
            key={t.id}
            onClick={() => setActive(t.id)}
            className={`group flex items-center gap-1 pl-2 pr-1 h-6 rounded cursor-pointer whitespace-nowrap ${
              t.id === active ? 'bg-[#2d2d2d] text-white' : 'text-gray-400 hover:text-gray-200'}`}
          >
            <span className="max-w-[12rem] truncate">{t.title}</span>
            {ended[t.id] && <span className="text-amber-300">·kết thúc</span>}
            <button
              onClick={e => { e.stopPropagation(); close(t.id) }}
              title="Đóng terminal (kết thúc shell)"
              className="w-4 text-gray-500 hover:text-white"
            >×</button>
          </div>
        ))}
        <button onClick={add} title="Terminal mới" className="px-1.5 text-gray-400 hover:text-white">+</button>
        <span className="ml-auto flex items-center gap-2 pl-2">
          {ended[active] && (
            <button onClick={() => restart(active)} className="text-sky-300 hover:underline">mở lại shell</button>
          )}
          <button onClick={onToggleMax} title={maximized ? 'Thu nhỏ' : 'Phóng to'} className="text-gray-400 hover:text-white">
            {maximized ? '▾' : '▴'}
          </button>
          <button onClick={onHide} title="Ẩn (Ctrl+`)" className="text-gray-400 hover:text-white">×</button>
        </span>
      </div>
      <div className="flex-1 min-h-0 relative">
        {tabs.map(t => (
          <TermView
            key={`${t.id}:${t.gen}`}
            channelID={channelID}
            shown={visible && t.id === active}
            onTitle={title => setTabs(ts => ts.map(x => x.id === t.id ? { ...x, title: title || 'shell' } : x))}
            onEnd={why => setEnded(e => ({ ...e, [t.id]: why }))}
          />
        ))}
      </div>
    </div>
  )
}

function TermView({ channelID, shown, onTitle, onEnd }: {
  channelID: string
  shown: boolean
  onTitle: (title: string) => void
  onEnd: (why: string) => void
}) {
  const box = useRef<HTMLDivElement>(null)
  const term = useRef<Terminal | null>(null)
  const fit = useRef<FitAddon | null>(null)
  const cb = useRef({ onTitle, onEnd })
  cb.current = { onTitle, onEnd }

  useEffect(() => {
    const el = box.current!
    const t = new Terminal({
      fontSize: 13,
      fontFamily: 'Menlo, Monaco, "SF Mono", Consolas, "Liberation Mono", monospace',
      cursorBlink: true,
      scrollback: 10000,
      allowProposedApi: false,
      theme: { background: '#1e1e1e', foreground: '#cccccc', cursor: '#aeafad', selectionBackground: '#264f78' },
    })
    const f = new FitAddon()
    t.loadAddon(f)
    t.loadAddon(new WebLinksAddon())
    t.open(el)
    try { f.fit() } catch {}
    term.current = t
    fit.current = f

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${location.host}/api/term?channel=${encodeURIComponent(channelID)}&cols=${t.cols}&rows=${t.rows}`
    const ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'
    const enc = new TextEncoder()
    let opened = false

    ws.onopen = () => { opened = true }
    ws.onmessage = ev => {
      if (typeof ev.data === 'string') t.write(ev.data)
      else t.write(new Uint8Array(ev.data as ArrayBuffer))
    }
    ws.onclose = ev => {
      const why = opened ? (ev.reason || 'shell đã thoát') : 'không mở được terminal (cần đăng nhập, hoặc dashboard cũ chưa có terminal)'
      t.write(`\r\n\x1b[2m[${why}]\x1b[0m\r\n`)
      cb.current.onEnd(why)
    }
    const send = (data: string) => { if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(data)) }
    const d1 = t.onData(send)
    const d2 = t.onBinary(data => {
      if (ws.readyState !== WebSocket.OPEN) return
      const bytes = new Uint8Array(data.length)
      for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 0xff
      ws.send(bytes)
    })
    const d3 = t.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    })
    const d4 = t.onTitleChange(title => cb.current.onTitle(title))

    // Refit whenever the panel changes size — dragged, maximized, window
    // resized — but not while hidden, when the box measures 0×0.
    const ro = new ResizeObserver(() => {
      if (el.offsetWidth > 0 && el.offsetHeight > 0) { try { f.fit() } catch {} }
    })
    ro.observe(el)

    return () => {
      ro.disconnect()
      d1.dispose(); d2.dispose(); d3.dispose(); d4.dispose()
      ws.onclose = null
      ws.close()
      t.dispose()
      term.current = null
    }
  }, [channelID])

  useEffect(() => {
    if (!shown) return
    const id = requestAnimationFrame(() => {
      try { fit.current?.fit() } catch {}
      term.current?.focus()
    })
    return () => cancelAnimationFrame(id)
  }, [shown])

  return <div ref={box} hidden={!shown} className="absolute inset-0 pl-2 pt-1" />
}
