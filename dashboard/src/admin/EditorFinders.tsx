import { useEffect, useMemo, useRef, useState } from 'react'

type Call = (method: string, params?: any) => Promise<any>

// Where to put the cursor once a file is open: a search hit's line and span.
export interface Reveal {
  line: number
  col: number
  len: number
}

const errText = (e: any) => String(e?.message ?? e)
const dirOf = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '')
const baseOf = (p: string) => p.slice(p.lastIndexOf('/') + 1)

// fuzzy scores path against query the way VS Code's Cmd+P feels: the letters
// in order, anywhere, with runs, word starts and the file name worth more than
// the folders above it. Null when the letters are not all there.
export function fuzzy(query: string, path: string): number | null {
  const q = query.toLowerCase().replace(/\s+/g, '')
  if (!q) return 0
  const s = path.toLowerCase()
  const base = s.lastIndexOf('/') + 1
  let qi = 0
  let score = 0
  let last = -2
  for (let i = 0; i < s.length && qi < q.length; i++) {
    if (s[i] !== q[qi]) continue
    score += 1
    if (i === last + 1) score += 3
    if (i >= base) score += 2
    const prev = s[i - 1]
    if (i === 0 || prev === '/' || prev === '_' || prev === '-' || prev === '.' || prev === ' ') score += 4
    last = i
    qi++
  }
  if (qi < q.length) return null
  return score - s.length * 0.01
}

// QuickOpen is Cmd+P: every file in the project, found by a few letters.
export function QuickOpen({ call, channelID, recent, onOpen, onClose }: {
  call: Call; channelID: string; recent: string[]
  onOpen: (path: string) => void; onClose: () => void
}) {
  const [paths, setPaths] = useState<string[] | null>(null)
  const [note, setNote] = useState('')
  const [q, setQ] = useState('')
  const [sel, setSel] = useState(0)
  const listRef = useRef<HTMLDivElement>(null)

  // Fetched on every open: agents add files all the time, and a list from ten
  // minutes ago would not have the one they just wrote.
  useEffect(() => {
    call('channels.files', { channel_id: channelID, op: 'index' })
      .then((r: any) => {
        setPaths(r?.paths ?? [])
        setNote(r?.truncated ? 'Danh sách bị cắt — dự án có quá nhiều file' : '')
      })
      .catch((e: any) => { setPaths([]); setNote(errText(e)) })
  }, [call, channelID])

  const results = useMemo(() => {
    if (!paths) return []
    if (!q.trim()) {
      const rest = paths.filter(p => !recent.includes(p))
      return [...recent.filter(p => paths.includes(p)), ...rest].slice(0, 60)
    }
    return paths
      .map(p => ({ p, s: fuzzy(q, p) }))
      .filter((x): x is { p: string; s: number } => x.s !== null)
      .sort((a, b) => b.s - a.s)
      .slice(0, 60)
      .map(x => x.p)
  }, [paths, q, recent])

  useEffect(() => { setSel(0) }, [q])
  useEffect(() => {
    listRef.current?.querySelector(`[data-i="${sel}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [sel])

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSel(i => Math.min(i + 1, results.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSel(i => Math.max(i - 1, 0)) }
    else if (e.key === 'Enter') { e.preventDefault(); if (results[sel]) onOpen(results[sel]) }
    else if (e.key === 'Escape') { e.preventDefault(); onClose() }
  }

  return (
    <div onMouseDown={onClose} className="absolute inset-0 z-20 flex justify-center pt-12 px-4 bg-black/30">
      <div
        onMouseDown={e => e.stopPropagation()}
        className="w-full max-w-xl h-fit max-h-[70vh] flex flex-col rounded-md bg-[#252526] ring-1 ring-black/60 shadow-2xl overflow-hidden"
      >
        <input
          autoFocus value={q} onChange={e => setQ(e.target.value)} onKeyDown={onKey}
          placeholder="Tìm file theo tên…"
          className="m-2 px-2 py-1.5 text-sm bg-[#3c3c3c] text-gray-100 rounded outline-none ring-1 ring-sky-700 placeholder:text-gray-500"
        />
        {note && <div className="px-3 pb-1 text-[11px] text-amber-300">{note}</div>}
        <div ref={listRef} className="overflow-y-auto pb-1">
          {paths === null ? (
            <div className="px-3 py-2 text-xs text-gray-500">Đang liệt kê file…</div>
          ) : results.length === 0 ? (
            <div className="px-3 py-2 text-xs text-gray-500">Không có file nào khớp.</div>
          ) : results.map((p, i) => (
            <div
              key={p} data-i={i}
              onMouseEnter={() => setSel(i)}
              onClick={() => onOpen(p)}
              className={`flex items-baseline gap-2 px-3 py-1 cursor-pointer text-[13px] ${i === sel ? 'bg-sky-900/60 text-white' : 'text-gray-300'}`}
            >
              <span className="shrink-0">{baseOf(p)}</span>
              <span className="truncate text-[11px] text-gray-500">{dirOf(p)}</span>
              {!q.trim() && recent.includes(p) && <span className="ml-auto text-[10px] text-gray-500">đang mở</span>}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

interface Match {
  path: string
  line: number
  col: number
  len: number
  text: string
  at: number
}

// SearchPanel is Cmd+Shift+F: a text across every file in the project.
export function SearchPanel({ call, channelID, focusKey, onOpen }: {
  call: Call; channelID: string
  focusKey: number // bumped by Cmd+Shift+F so the box takes focus again
  onOpen: (path: string, at: Reveal) => void
}) {
  const [q, setQ] = useState('')
  const [caseSensitive, setCase] = useState(false)
  const [wholeWord, setWord] = useState(false)
  const [regex, setRegex] = useState(false)
  const [matches, setMatches] = useState<Match[] | null>(null)
  const [info, setInfo] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const inputRef = useRef<HTMLInputElement>(null)
  const seq = useRef(0)

  useEffect(() => { inputRef.current?.focus(); inputRef.current?.select() }, [focusKey])

  const run = async (query = q) => {
    if (!query.trim()) { setMatches(null); setInfo(''); setErr(null); return }
    const mine = ++seq.current
    setBusy(true)
    try {
      const r = await call('channels.files', {
        channel_id: channelID, op: 'search', query,
        case_sensitive: caseSensitive, whole_word: wholeWord, regex,
      })
      if (mine !== seq.current) return // a newer search already answered
      const m: Match[] = r?.matches ?? []
      const files = new Set(m.map(x => x.path)).size
      const bits = [`${m.length}${r?.truncated ? '+' : ''} kết quả trong ${files} file`]
      if (r?.skipped) bits.push(`bỏ qua ${r.skipped} file > 1 MB`)
      setMatches(m)
      setInfo(bits.join(' · '))
      setErr(null)
    } catch (e) {
      if (mine !== seq.current) return
      setErr(errText(e))
      setMatches(null)
      setInfo('')
    } finally {
      if (mine === seq.current) setBusy(false)
    }
  }

  // Search as you type, a beat after the last key, and again when a toggle
  // changes what the same text means.
  useEffect(() => {
    const t = setTimeout(() => run(), 350)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, caseSensitive, wholeWord, regex])

  const groups = useMemo(() => {
    const out: { path: string; items: Match[] }[] = []
    for (const m of matches ?? []) {
      const last = out[out.length - 1]
      if (last && last.path === m.path) last.items.push(m)
      else out.push({ path: m.path, items: [m] })
    }
    return out
  }, [matches])

  const toggle = (on: boolean, set: (v: boolean) => void, label: string, title: string) => (
    <button
      onClick={() => set(!on)} title={title}
      className={`px-1 rounded text-[11px] font-mono ${on ? 'bg-sky-800 text-white' : 'text-gray-400 hover:text-white'}`}
    >{label}</button>
  )

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      <div className="px-3 pb-2 shrink-0">
        <div className="flex items-center gap-1 rounded bg-[#3c3c3c] ring-1 ring-transparent focus-within:ring-sky-700 pr-1">
          <input
            ref={inputRef} value={q}
            onChange={e => setQ(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') run() }}
            placeholder="Tìm trong mọi file"
            className="flex-1 min-w-0 px-2 py-1 text-[13px] bg-transparent text-gray-100 outline-none placeholder:text-gray-500"
          />
          {toggle(caseSensitive, setCase, 'Aa', 'Phân biệt hoa thường')}
          {toggle(wholeWord, setWord, 'ab', 'Nguyên từ')}
          {toggle(regex, setRegex, '.*', 'Biểu thức chính quy')}
        </div>
        <div className="mt-1 text-[11px] text-gray-500 min-h-[1rem]">
          {busy ? 'đang tìm…' : err ? <span className="text-red-300">{err}</span> : info}
        </div>
      </div>
      <div className="flex-1 overflow-y-auto pb-4">
        {groups.map(g => (
          <div key={g.path}>
            <div
              onClick={() => setCollapsed(s => { const n = new Set(s); n.has(g.path) ? n.delete(g.path) : n.add(g.path); return n })}
              title={g.path}
              className="flex items-center gap-1 px-2 py-[3px] text-[13px] cursor-pointer text-gray-200 hover:bg-gray-800/50"
            >
              <span className="w-3 text-[10px] text-gray-500">{collapsed.has(g.path) ? '▸' : '▾'}</span>
              <span className="shrink-0">{baseOf(g.path)}</span>
              <span className="truncate text-[11px] text-gray-500">{dirOf(g.path)}</span>
              <span className="ml-auto shrink-0 px-1.5 rounded-full bg-gray-700 text-[10px] text-gray-200">{g.items.length}</span>
            </div>
            {!collapsed.has(g.path) && g.items.map((m, i) => {
              // The panel is narrow: a few characters of lead-in, so the match
              // itself is what shows, not the start of a long line.
              const chars = [...m.text]
              const from = Math.max(0, m.at - 14)
              const lead = (from > 0 ? '…' : '') + chars.slice(from, m.at).join('').trimStart()
              return (
                <div
                  key={i}
                  onClick={() => onOpen(m.path, { line: m.line, col: m.col, len: m.len })}
                  title={`${m.path}:${m.line}`}
                  className="flex gap-2 pl-7 pr-2 py-[2px] text-[12px] cursor-pointer text-gray-400 hover:bg-gray-800/50 whitespace-nowrap overflow-hidden"
                >
                  <span className="shrink-0 w-8 text-right text-gray-600">{m.line}</span>
                  <span className="truncate font-mono">
                    {lead}
                    <mark className="bg-amber-500/40 text-gray-100 rounded-sm">{chars.slice(m.at, m.at + m.len).join('')}</mark>
                    {chars.slice(m.at + m.len).join('')}
                  </span>
                </div>
              )
            })}
          </div>
        ))}
      </div>
    </div>
  )
}
