import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FollowUp, Task, TaskLogEntry, TaskSession as Session } from './types'
import MessageMarkdown from '../components/MessageMarkdown'

type Call = (method: string, params?: any) => Promise<any>

// The task's session, the way a coding-agent board shows an attempt: what the
// agent was told, what it said, each tool it ran, and a box at the bottom to
// talk to it. Modelled on vibe-kanban's attempt view.
//
// Everything here comes from the shared database (tasks.log), so it reads the
// same whichever agent ran the task and whatever its CLI.

const TERMINAL = ['completed', 'failed', 'canceled', 'rejected']

const clock = (iso: string) => {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

const END_LABEL: Record<string, { text: string; cls: string }> = {
  completed: { text: 'xong', cls: 'text-emerald-300 ring-emerald-500/30 bg-emerald-500/5' },
  answered: { text: 'đã trả lời', cls: 'text-emerald-300 ring-emerald-500/30 bg-emerald-500/5' },
  advanced: { text: 'chạy tiếp lượt sau', cls: 'text-sky-300 ring-sky-500/30 bg-sky-500/5' },
  plan_only: { text: 'mới lên kế hoạch', cls: 'text-sky-300 ring-sky-500/30 bg-sky-500/5' },
  blocked: { text: 'chờ', cls: 'text-violet-300 ring-violet-500/30 bg-violet-500/5' },
  empty: { text: 'không ra gì', cls: 'text-amber-300 ring-amber-500/30 bg-amber-500/5' },
  timed_out: { text: 'hết giờ', cls: 'text-amber-300 ring-amber-500/30 bg-amber-500/5' },
  canceled: { text: 'đã huỷ', cls: 'text-gray-400 ring-gray-600/40 bg-gray-500/5' },
  failed: { text: 'lỗi', cls: 'text-red-300 ring-red-500/30 bg-red-500/5' },
  lost: { text: 'mất kết nối', cls: 'text-gray-400 ring-gray-600/40 bg-gray-500/5' },
}

const FOLLOWUP_LABEL: Record<FollowUp['state'], { text: string; cls: string }> = {
  queued: { text: 'đang chờ agent', cls: 'text-amber-300' },
  delivered: { text: 'đã đưa vào lượt chạy', cls: 'text-sky-300' },
  running: { text: 'agent đang trả lời…', cls: 'text-amber-300' },
  answered: { text: 'đã trả lời', cls: 'text-emerald-300' },
  failed: { text: 'lỗi khi trả lời', cls: 'text-red-300' },
}

/** A tool call and, when the CLI reported one, its output. */
type Row =
  | { type: 'entry'; e: TaskLogEntry }
  | { type: 'tool'; e: TaskLogEntry; result?: TaskLogEntry }

function rows(entries: TaskLogEntry[]): Row[] {
  const out: Row[] = []
  for (const e of entries) {
    if (e.kind === 'tool_result') {
      const last = out[out.length - 1]
      if (last && last.type === 'tool' && !last.result) { last.result = e; continue }
      out.push({ type: 'tool', e: { ...e, kind: 'tool', label: e.label || 'kết quả', content: '' }, result: e })
      continue
    }
    out.push(e.kind === 'tool' ? { type: 'tool', e } : { type: 'entry', e })
  }
  return out
}

function Collapsible({ head, children, defaultOpen = false, tone = 'gray' }: {
  head: React.ReactNode; children: React.ReactNode; defaultOpen?: boolean; tone?: 'gray' | 'sky'
}) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className={`rounded-md ring-1 ${tone === 'sky' ? 'ring-sky-500/20 bg-sky-500/5' : 'ring-gray-800 bg-gray-900/50'}`}>
      <button onClick={() => setOpen(o => !o)} className="w-full flex items-center gap-2 px-2.5 py-1.5 text-left text-xs">
        <span className="text-gray-600 w-3 shrink-0">{open ? '▾' : '▸'}</span>
        {head}
      </button>
      {open && <div className="px-2.5 pb-2">{children}</div>}
    </div>
  )
}

function ToolRow({ e, result }: { e: TaskLogEntry; result?: TaskLogEntry }) {
  const [open, setOpen] = useState(false)
  const failed = result?.label === 'error'
  const input = prettyInput(e.content ?? '')
  const expandable = !!input || !!result?.content
  return (
    <div className="text-xs">
      <button
        onClick={() => expandable && setOpen(o => !o)}
        className={`w-full flex items-center gap-2 px-2 py-1 rounded text-left ${expandable ? 'hover:bg-gray-900' : 'cursor-default'}`}
      >
        <span className={failed ? 'text-red-400' : 'text-gray-600'}>{failed ? '✕' : '⚙'}</span>
        <span className="font-mono text-gray-300 truncate">{e.label || 'tool'}</span>
        {result && !failed && <span className="text-emerald-500/70 shrink-0">✓</span>}
        <span className="ml-auto text-[10px] text-gray-600 shrink-0">{clock(e.created_at)}</span>
      </button>
      {open && (
        <div className="ml-6 mt-1 mb-1.5 space-y-1">
          {input && (
            <pre className="text-[11px] text-gray-400 bg-gray-900 rounded p-2 ring-1 ring-gray-800 whitespace-pre-wrap break-words max-h-64 overflow-auto">{input}</pre>
          )}
          {result?.content && (
            <pre className={`text-[11px] rounded p-2 ring-1 whitespace-pre-wrap break-words max-h-64 overflow-auto ${failed ? 'text-red-300 bg-red-500/5 ring-red-500/20' : 'text-gray-300 bg-black/40 ring-gray-800'}`}>{result.content}</pre>
          )}
        </div>
      )}
    </div>
  )
}

// Tool inputs are JSON; one field ("command", "file_path") is usually the
// whole story, and a raw object is harder to read than the thing itself.
function prettyInput(raw: string): string {
  if (!raw) return ''
  try {
    const v = JSON.parse(raw)
    if (v && typeof v === 'object') {
      if (typeof v.command === 'string' && Object.keys(v).length <= 3) return v.command
      return JSON.stringify(v, null, 2)
    }
  } catch { /* not JSON: show as is */ }
  return raw
}

function Entry({ e, followUp }: { e: TaskLogEntry; followUp?: FollowUp }) {
  switch (e.kind) {
    case 'status': {
      if (e.label === 'start') {
        return (
          <div className="flex items-center gap-2 pt-3 pb-1 text-[11px] text-gray-500">
            <span className="h-px flex-1 bg-gray-800" />
            <span>{e.content}</span>
            <span className="text-gray-600">{clock(e.created_at)}</span>
            <span className="h-px flex-1 bg-gray-800" />
          </div>
        )
      }
      const how = (e.label ?? '').replace(/^end:/, '')
      const l = END_LABEL[how] ?? { text: how, cls: 'text-gray-400 ring-gray-700 bg-gray-500/5' }
      return (
        <div className="flex items-baseline gap-2 text-[11px]">
          <span className={`px-1.5 py-0.5 rounded ring-1 ${l.cls}`}>■ {l.text}</span>
          {e.content && <span className="text-gray-500 truncate" title={e.content}>{e.content}</span>}
        </div>
      )
    }
    case 'prompt':
      return (
        <Collapsible tone="sky" head={<>
          <span className="text-sky-300">Brief gửi agent</span>
          <span className="text-gray-500 truncate">{(e.content ?? '').split('\n').find(l => l.trim()) ?? ''}</span>
        </>}>
          <pre className="text-[11px] text-gray-300 whitespace-pre-wrap break-words max-h-96 overflow-auto">{e.content}</pre>
        </Collapsible>
      )
    case 'thinking':
      return (
        <Collapsible head={<>
          <span className="text-gray-500 italic">💭 suy nghĩ</span>
          <span className="text-gray-600 truncate italic">{(e.content ?? '').trim().slice(0, 90)}</span>
        </>}>
          <div className="text-xs text-gray-400 italic whitespace-pre-wrap">{e.content}</div>
        </Collapsible>
      )
    case 'user': {
      const st = followUp ? FOLLOWUP_LABEL[followUp.state] : undefined
      return (
        <div className="flex justify-end">
          <div className="max-w-[85%]">
            <div className="rounded-lg rounded-br-sm bg-sky-600/20 ring-1 ring-sky-500/30 px-3 py-2 text-sm text-gray-100 whitespace-pre-wrap break-words">
              {e.content}
            </div>
            <div className="mt-0.5 text-right text-[10px] text-gray-600">
              bạn · {clock(e.created_at)}
              {st && <span className={`ml-1 ${st.cls}`}>· {st.text}</span>}
            </div>
          </div>
        </div>
      )
    }
    case 'text':
    default:
      return (
        <div className="text-sm text-gray-200">
          <MessageMarkdown>{e.content ?? ''}</MessageMarkdown>
        </div>
      )
  }
}

/** What the box at the bottom does, given where the task is. */
function composerMode(task: Task, running: boolean): { label: string; hint: string; disabled?: boolean } {
  if (task.state === 'blocked' && task.blocked_on === 'human') {
    return { label: 'Trả lời & chạy tiếp', hint: 'Agent đang chờ bạn — câu trả lời mở khoá task và nó chạy tiếp.' }
  }
  if (TERMINAL.includes(task.state)) {
    if (!task.claimed_by) return { label: 'Gửi', hint: 'Task kết thúc trước khi có agent nào chạy — không có phiên để nói tiếp.', disabled: true }
    return { label: 'Hỏi tiếp', hint: `Task đã ${task.state}. ${task.claimed_by} mở lại đúng phiên này, trả lời một lượt; task giữ nguyên trạng thái.` }
  }
  if (running) {
    return { label: 'Xếp hàng', hint: 'Agent đang chạy. Tin được đưa vào lượt kế tiếp — hoặc nếu lượt này kết thúc task, agent trả lời ngay sau đó.' }
  }
  if (task.state === 'blocked') {
    return { label: 'Gửi', hint: 'Task đang chờ việc con. Tin được đưa vào lượt chạy kế tiếp.' }
  }
  return { label: 'Gửi', hint: 'Agent đọc tin này ở lượt chạy kế tiếp.' }
}

export default function TaskSession({ call, task, onChanged, threadRoot, onOpenThread }: {
  call: Call
  task: Task
  onChanged: () => void
  threadRoot?: string
  onOpenThread?: (rootID: string) => void
}) {
  const [entries, setEntries] = useState<TaskLogEntry[]>([])
  const [followUps, setFollowUps] = useState<FollowUp[]>([])
  const [running, setRunning] = useState(false)
  const [legacy, setLegacy] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [showThinking, setShowThinking] = useState(true)
  const scroller = useRef<HTMLDivElement>(null)
  const stick = useRef(true) // follow the bottom unless the reader scrolled up
  const lastID = useRef(0)
  const legacyRef = useRef(false)

  // Reset when another task is opened in the same drawer.
  useEffect(() => {
    setEntries([]); setFollowUps([]); setLoaded(false); setErr(null)
    lastID.current = 0; legacyRef.current = false; stick.current = true
  }, [task.id])

  const load = useCallback(async () => {
    try {
      // A rebuilt (legacy) session has no cursor to continue from: read it whole.
      const after = legacyRef.current ? 0 : lastID.current
      const s: Session = await call('tasks.log', { id: task.id, after })
      const fresh = s.entries ?? []
      if (s.legacy || legacyRef.current || after === 0) {
        setEntries(fresh)
      } else if (fresh.length) {
        setEntries(prev => [...prev, ...fresh])
      }
      for (const e of fresh) if (e.id > lastID.current) lastID.current = e.id
      legacyRef.current = !!s.legacy
      setLegacy(!!s.legacy)
      setFollowUps(s.followups ?? [])
      setRunning(s.running)
      setLoaded(true)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call, task.id])

  useEffect(() => { void load() }, [load])

  // Poll fast while something is moving — a run, a turn, a line waiting for
  // an agent — and slowly otherwise, so a run that starts is noticed.
  const pending = followUps.some(f => f.state === 'queued' || f.state === 'running')
  const open = !TERMINAL.includes(task.state)
  const interval = running || pending ? 1500 : open ? 5000 : 0
  useEffect(() => {
    if (!interval) return
    const t = setInterval(() => { void load() }, interval)
    return () => clearInterval(t)
  }, [interval, load])

  // The board's own copy of the task (state, live) trails the log; nudge it
  // when a run starts or stops so the left column agrees with this one.
  const wasRunning = useRef(running)
  useEffect(() => {
    if (wasRunning.current !== running) onChanged()
    wasRunning.current = running
  }, [running, onChanged])

  useEffect(() => {
    const el = scroller.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [entries, followUps])

  const byID = useMemo(() => new Map(followUps.map(f => [f.id, f])), [followUps])
  const shown = useMemo(
    () => rows(showThinking ? entries : entries.filter(e => e.kind !== 'thinking')),
    [entries, showThinking],
  )
  const lastTool = useMemo(() => [...entries].reverse().find(e => e.kind === 'tool'), [entries])

  const mode = composerMode(task, running)
  // A mention lives in its room thread: that is where it was asked and where
  // a reply reaches the agent. There is no separate session to type into.
  const inThread = task.kind === 'mention' && !!threadRoot

  const send = async () => {
    const text = draft.trim()
    if (!text || sending || mode.disabled) return
    setSending(true)
    setErr(null)
    try {
      await call('tasks.followup', { id: task.id, message: text })
      setDraft('')
      stick.current = true
      await load()
      onChanged()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="px-4 py-2 border-b border-gray-800 flex items-center gap-2 text-xs">
        <span className="font-medium text-gray-200">Phiên làm việc</span>
        {running
          ? <span className="flex items-center gap-1 text-amber-300"><span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />đang chạy</span>
          : <span className="text-gray-600">{open ? 'đang chờ lượt chạy' : 'đã dừng'}</span>}
        {legacy && <span className="text-gray-600" title="Lượt chạy trước khi có log phiên: chỉ có tool call và câu trả lời cuối, không có lời ở giữa.">· dựng lại từ trace</span>}
        <label className="ml-auto flex items-center gap-1 text-gray-500 cursor-pointer select-none">
          <input type="checkbox" checked={showThinking} onChange={e => setShowThinking(e.target.checked)} className="accent-sky-500" />
          suy nghĩ
        </label>
      </div>

      <div
        ref={scroller}
        onScroll={e => {
          const el = e.currentTarget
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
        }}
        className="flex-1 min-h-0 overflow-y-auto px-4 py-3 space-y-2"
      >
        {!loaded && !err && <div className="text-sm text-gray-500">Đang tải phiên…</div>}
        {loaded && entries.length === 0 && (
          <div className="text-sm text-gray-500 py-8 text-center">
            {task.state === 'submitted'
              ? 'Chưa có agent nào nhận task này. Khi nó chạy, mọi bước sẽ hiện ở đây.'
              : 'Chưa có gì được ghi lại cho task này.'}
          </div>
        )}
        {shown.map(r => r.type === 'tool'
          ? <ToolRow key={r.e.id} e={r.e} result={r.result} />
          : <Entry key={r.e.id} e={r.e} followUp={r.e.kind === 'user' && r.e.run_id ? byID.get(r.e.run_id) : undefined} />)}
        {running && (
          <div className="flex items-center gap-2 text-xs text-amber-300/90 pt-1">
            <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />
            <span>agent đang làm</span>
            {lastTool?.label && <span className="font-mono text-gray-400 truncate">· {lastTool.label}</span>}
          </div>
        )}
      </div>

      <div className="border-t border-gray-800 p-3">
        {err && <div className="mb-2 text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2">{err}</div>}
        {inThread ? (
          <button
            onClick={() => onOpenThread?.(threadRoot!)}
            className="w-full px-3 py-2 text-sm rounded ring-1 ring-sky-500/40 bg-sky-500/10 text-sky-200 hover:bg-sky-500/20"
          >
            Việc này nằm trong thread của phòng — mở thread để nhắn tiếp →
          </button>
        ) : (
          <>
            <div className={`rounded-lg ring-1 bg-gray-900 ${mode.disabled ? 'ring-gray-800 opacity-60' : 'ring-gray-700 focus-within:ring-sky-500/60'}`}>
              <textarea
                value={draft}
                onChange={e => setDraft(e.target.value)}
                onKeyDown={e => {
                  // Enter sends, Shift+Enter is a new line; IME composition
                  // (Vietnamese Telex) must not send half a word.
                  if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                    e.preventDefault()
                    void send()
                  }
                }}
                disabled={mode.disabled || sending}
                rows={2}
                placeholder={mode.disabled ? '' : 'Nhắn cho agent trong phiên này…  (Enter gửi · Shift+Enter xuống dòng)'}
                className="w-full bg-transparent px-3 pt-2 text-sm text-gray-100 placeholder-gray-600 outline-none resize-none max-h-48"
              />
              <div className="flex items-center gap-2 px-2 pb-2">
                <span className="text-[11px] text-gray-500 leading-snug flex-1">{mode.hint}</span>
                <button
                  onClick={send}
                  disabled={mode.disabled || sending || !draft.trim()}
                  className="shrink-0 px-3 py-1.5 text-xs rounded-md bg-sky-500 text-white font-medium hover:bg-sky-400 disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  {sending ? 'Đang gửi…' : mode.label}
                </button>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
