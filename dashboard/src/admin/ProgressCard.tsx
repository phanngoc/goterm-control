import { useEffect, useState } from 'react'

// The live status of an agent mid-turn, drawn the way Claude Code draws one: a
// spinner and a clock that ticks on its own, the step running now with the few
// before it, the newest thinking, the plan, and the answer as it forms. Kept to
// a fixed, small height so it can sit pinned at the bottom of a thread and
// always be in view; everything else is a click away.
//
// The data comes from the progress line itself (coord.WithProgress): a JSON
// comment after the markdown. The clock is computed here from `since`, so it
// moves every second even though the line is only redrawn every few.

export interface ProgressTodo { content: string; status: string; active?: string }
export interface Progress {
  agent?: string
  label: string
  task?: string
  since?: string
  steps?: string[]
  step_count: number
  thinking?: string
  todos?: ProgressTodo[]
  partial?: string
}

const MARKER = '<!--bomclaw:progress '

export function parseProgress(body: string): Progress | null {
  const i = body.lastIndexOf(MARKER)
  if (i < 0) return null
  const raw = body.slice(i + MARKER.length).trim().replace(/-->$/, '')
  try { return JSON.parse(raw) as Progress } catch { return null }
}

export function isProgressLine(body: string): boolean {
  return body.startsWith('⏳ ')
}

// The frames Claude Code cycles through while it works — less ✳, which most
// fonts draw as a green emoji square.
const FRAMES = ['·', '✢', '✶', '✻', '✽', '✻', '✶', '✢']

export function Spinner({ className = '' }: { className?: string }) {
  const [i, setI] = useState(0)
  useEffect(() => {
    const t = setInterval(() => setI(n => (n + 1) % FRAMES.length), 120)
    return () => clearInterval(t)
  }, [])
  return <span className={`inline-block w-3 text-center text-amber-400 ${className}`}>{FRAMES[i]}</span>
}

export function useElapsed(since?: string): string {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  if (!since) return ''
  const start = new Date(since).getTime()
  if (!start || start < 0 || isNaN(start)) return ''
  const s = Math.max(0, Math.floor((now - start) / 1000))
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60
  return h ? `${h}h${String(m).padStart(2, '0')}m` : m ? `${m}m${String(sec).padStart(2, '0')}s` : `${sec}s`
}

/** ProgressSummary is the one-line form for a room's main list. */
export function ProgressSummary({ p, fallbackSince }: { p: Progress; fallbackSince?: string }) {
  const elapsed = useElapsed(validSince(p.since) ?? fallbackSince)
  const step = p.steps?.length ? p.steps[p.steps.length - 1] : ''
  return (
    <div className="flex items-center gap-1.5 text-xs text-gray-300 min-w-0">
      <Spinner />
      <span className="font-mono text-sky-300 shrink-0">{p.agent}</span>
      <span className="text-amber-300/90 shrink-0">{p.label}</span>
      {elapsed && <span className="text-gray-500 shrink-0 tabular-nums">· {elapsed}</span>}
      {step
        ? <span className="font-mono text-gray-400 truncate">· ▸ {step}</span>
        : p.thinking ? <span className="italic text-gray-500 truncate">· {p.thinking}</span> : null}
    </div>
  )
}

export default function ProgressCard({ p, author, fallbackSince }: {
  p: Progress; author: string; fallbackSince?: string
}) {
  const [open, setOpen] = useState(false)
  const elapsed = useElapsed(validSince(p.since) ?? fallbackSince)
  const steps = p.steps ?? []
  const current = steps.length ? steps[steps.length - 1] : ''
  const before = steps.slice(0, -1)
  const shownBefore = open ? before : before.slice(-2)
  const earlier = p.step_count - steps.length + (before.length - shownBefore.length)
  const todos = p.todos ?? []
  const done = todos.filter(t => t.status === 'completed').length
  const now = todos.find(t => t.status === 'in_progress')

  return (
    <div className="rounded-lg bg-gray-950/80 ring-1 ring-amber-500/25 px-3 py-2 text-xs shadow-lg shadow-black/30">
      {/* Header: who, what, how long — the line you glance at. */}
      <div className="flex items-center gap-1.5 min-w-0">
        <Spinner className="text-sm" />
        <span className="font-mono text-gray-200">{p.agent || author}</span>
        <span className="text-amber-300">{p.label}…</span>
        {p.task && <span className="font-mono text-fuchsia-300/80 truncate">{p.task.slice(0, 10)}</span>}
        <span className="ml-auto shrink-0 tabular-nums text-gray-400">
          {elapsed}{p.step_count > 0 && <span className="text-gray-600"> · {p.step_count} bước</span>}
        </span>
        <button
          onClick={() => setOpen(v => !v)}
          className="shrink-0 ml-1 px-1.5 rounded text-gray-500 hover:text-gray-200 hover:bg-gray-800"
          title={open ? 'Thu gọn' : 'Xem chi tiết'}
        >
          {open ? '▾' : '▸'}
        </button>
      </div>

      {/* Thinking: dim and italic, the newest paragraph only. */}
      {p.thinking && (
        <div className={`mt-1.5 pl-4 italic text-gray-400 break-words ${open ? '' : 'line-clamp-2'}`}>
          ✻ {p.thinking}
        </div>
      )}

      {/* Steps: the running one last, with the spinner; a couple before it. */}
      {(current || earlier > 0) && (
        <div className={`mt-1.5 font-mono space-y-0.5 ${open ? 'max-h-48 overflow-y-auto' : ''}`}>
          {earlier > 0 && <div className="pl-4 text-gray-600">… {earlier} bước trước</div>}
          {shownBefore.map((s, i) => (
            <div key={i} className="pl-4 text-gray-500 truncate"><span className="text-emerald-500/70">✓</span> {s}</div>
          ))}
          {current && (
            <div className="flex items-center gap-1 text-gray-100 min-w-0">
              <Spinner />
              <span className="truncate">{current}</span>
            </div>
          )}
        </div>
      )}

      {/* Plan: a count and the item in hand; the whole list when open. */}
      {todos.length > 0 && (
        <div className="mt-1.5 pl-4 text-gray-400">
          <span className="text-gray-500">Kế hoạch {done}/{todos.length}</span>
          {!open && now && <span className="text-gray-200"> · ◐ {now.active || now.content}</span>}
          {open && (
            <ul className="mt-0.5 space-y-0.5">
              {todos.map((t, i) => (
                <li key={i} className={
                  t.status === 'completed' ? 'line-through text-gray-600'
                    : t.status === 'in_progress' ? 'text-gray-100' : 'text-gray-400'
                }>
                  {t.status === 'completed' ? '☑' : t.status === 'in_progress' ? '◐' : '☐'} {t.status === 'in_progress' ? (t.active || t.content) : t.content}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {/* The answer as it forms: a few lines, faded at the cut. */}
      {p.partial && (
        <div className={`mt-1.5 pl-4 text-gray-300 whitespace-pre-wrap break-words ${
          open ? 'max-h-60 overflow-y-auto' : 'line-clamp-3'
        }`}>
          {p.partial}
        </div>
      )}
    </div>
  )
}

function validSince(s?: string): string | undefined {
  if (!s || s.startsWith('0001-')) return undefined // Go's zero time
  return s
}
