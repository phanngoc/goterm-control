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
  steered: { text: 'bạn đã dừng để chỉ hướng', cls: 'text-amber-300 ring-amber-500/30 bg-amber-500/5' },
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

// ── Rendering, the way Claude Code prints a session ────────────────────────
//
//   ⏺ Bash(cd games/tank-battle && npm test)
//     ⎿  1 failing: laser should not break bricks
//   ⏺ Chạy 6 lệnh, đọc 2 file, ghi 1 file  ▸
//   ✻ Suy nghĩ  cần đọc file thiết kế trước…
//
// One line per step, its argument right there, output indented under it.
// A burst of tool calls folds into one summary line: a glance says what was
// called, a click says how.

/** A tool call and, when the CLI reported one, its output. */
type ToolItem = { e: TaskLogEntry; result?: TaskLogEntry }
type Row =
  | { type: 'entry'; e: TaskLogEntry }
  | { type: 'tools'; key: number; items: ToolItem[] }

/** Consecutive tool calls fold into one group once there are this many. */
const GROUP_AT = 3

function rows(entries: TaskLogEntry[]): Row[] {
  const out: Row[] = []
  const tools = (): ToolItem[] | null => {
    const last = out[out.length - 1]
    return last && last.type === 'tools' ? last.items : null
  }
  for (const e of entries) {
    if (e.kind === 'tool_result') {
      const items = tools()
      const lastItem = items?.[items.length - 1]
      if (lastItem && !lastItem.result) { lastItem.result = e; continue }
      out.push({ type: 'tools', key: e.id, items: [{ e: { ...e, kind: 'tool', label: e.label || 'kết quả', content: '' }, result: e }] })
      continue
    }
    if (e.kind === 'tool') {
      const items = tools()
      if (items) items.push({ e })
      else out.push({ type: 'tools', key: e.id, items: [{ e }] })
      continue
    }
    out.push({ type: 'entry', e })
  }
  return out
}

// The argument worth showing for a call, read from its input: the whole
// command, the path, the URL. The stored label is shortened to fit a status
// line; a row has the width for the real thing, and CSS cuts what does not fit.
const ARG_KEYS = ['command', 'file_path', 'path', 'url', 'query', 'pattern', 'description', 'prompt', 'script', 'expression', 'name', 'ref', 'text', 'glob', 'regex']

function toolHead(e: TaskLogEntry): { name: string; arg: string } {
  const label = e.label ?? ''
  const paren = label.indexOf('(')
  const name = (paren > 0 ? label.slice(0, paren) : label) || 'Tool'
  let arg = paren > 0 && label.endsWith(')') ? label.slice(paren + 1, -1) : ''
  try {
    const v = JSON.parse(e.content ?? '')
    if (v && typeof v === 'object') {
      for (const k of ARG_KEYS) {
        if (typeof v[k] === 'string' && v[k].trim()) {
          arg = v[k].trim().split('\n')[0]
          if (k === 'file_path' || k === 'path') arg = arg.replace(/^\/Users\/[^/]+\//, '~/')
          break
        }
      }
    }
  } catch { /* not JSON: keep the label's own argument */ }
  return { name, arg }
}

// Tool inputs are JSON; one field is usually the whole story.
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

const VERBS: Record<string, [string, string]> = {
  Bash: ['Chạy', 'lệnh'], shell: ['Chạy', 'lệnh'], exec_command: ['Chạy', 'lệnh'],
  Read: ['Đọc', 'file'], Write: ['Ghi', 'file'], Edit: ['Sửa', 'file'], MultiEdit: ['Sửa', 'file'],
  NotebookEdit: ['Sửa', 'notebook'], Grep: ['Tìm', 'lần'], Glob: ['Tìm', 'lần'],
  WebFetch: ['Mở', 'trang web'], WebSearch: ['Tra', 'lần web'], Task: ['Giao', 'sub-agent'],
  Agent: ['Giao', 'sub-agent'], TodoWrite: ['Cập nhật kế hoạch', ''],
}

/** "Chạy 6 lệnh, đọc 2 file, ghi 1 file" — in the order they were first used. */
function summarize(items: ToolItem[]): string {
  const counts = new Map<string, number>()
  for (const it of items) {
    const n = toolHead(it.e).name
    counts.set(n, (counts.get(n) ?? 0) + 1)
  }
  const parts: string[] = []
  // Same verb across tools (Edit + MultiEdit) adds up.
  const byVerb = new Map<string, number>()
  const order: string[] = []
  for (const [name, n] of counts) {
    const v = VERBS[name]
    const key = v ? `${v[0]}|${v[1]}` : `|${name}`
    if (!byVerb.has(key)) order.push(key)
    byVerb.set(key, (byVerb.get(key) ?? 0) + n)
  }
  for (const key of order) {
    const [verb, noun] = key.split('|')
    const n = byVerb.get(key)!
    if (!verb) parts.push(n > 1 ? `${noun} ×${n}` : noun)
    else if (!noun) parts.push(n > 1 ? `${verb} ×${n}` : verb)
    else parts.push(`${verb} ${n} ${noun}`)
  }
  const s = parts.join(', ')
  return s.charAt(0).toUpperCase() + s.slice(1).replace(/, ([A-ZĐ])/g, (_, c) => ', ' + c.toLowerCase())
}

const Dot = ({ cls }: { cls: string }) => <span className={`w-3 shrink-0 text-center text-[10px] leading-5 ${cls}`}>●</span>

function ToolLine({ item, live }: { item: ToolItem; live?: boolean }) {
  const [open, setOpen] = useState(false)
  const { e, result } = item
  const { name, arg } = toolHead(e)
  const failed = result?.label === 'error'
  const input = prettyInput(e.content ?? '')
  const out = (result?.content ?? '').trim()
  const outLines = out ? out.split('\n') : []
  return (
    <div className="font-mono text-[12px] leading-5">
      <button
        onClick={() => setOpen(o => !o)}
        title={`${clock(e.created_at)} — bấm để xem đầy đủ`}
        className="w-full flex items-start gap-1.5 text-left hover:bg-gray-900/70 rounded px-1 -mx-1"
      >
        <Dot cls={live ? 'text-amber-400 animate-pulse' : failed ? 'text-red-400' : result ? 'text-emerald-500' : 'text-gray-500'} />
        <span className="min-w-0 truncate">
          <span className="font-semibold text-gray-100">{name}</span>
          {arg && <span className="text-gray-400">({arg})</span>}
        </span>
      </button>
      {!open && outLines.length > 0 && (
        <div className="flex gap-1.5 pl-[18px] text-gray-500">
          <span className="shrink-0">⎿</span>
          <span className={`min-w-0 truncate ${failed ? 'text-red-300/80' : ''}`}>
            {outLines[0]}
            {outLines.length > 1 && <span className="text-gray-600"> … +{outLines.length - 1} dòng</span>}
          </span>
        </div>
      )}
      {open && (
        <div className="pl-[18px] mt-0.5 mb-1 space-y-1">
          {input && <pre className="text-[11px] text-gray-400 bg-gray-900 rounded p-2 ring-1 ring-gray-800 whitespace-pre-wrap break-words max-h-64 overflow-auto">{input}</pre>}
          {out && (
            <div className="flex gap-1.5">
              <span className="shrink-0 text-gray-500">⎿</span>
              <pre className={`flex-1 min-w-0 text-[11px] rounded p-2 ring-1 whitespace-pre-wrap break-words max-h-64 overflow-auto ${failed ? 'text-red-300 bg-red-500/5 ring-red-500/20' : 'text-gray-300 bg-black/40 ring-gray-800'}`}>{out}</pre>
            </div>
          )}
          {!input && !out && <div className="text-[11px] text-gray-600">không có chi tiết</div>}
        </div>
      )}
    </div>
  )
}

/** A burst of calls: one summary line, the calls themselves a click away.
 *  The newest call stays visible while the run is still on it. */
function ToolGroup({ items, live }: { items: ToolItem[]; live: boolean }) {
  const [open, setOpen] = useState(false)
  if (items.length < GROUP_AT) {
    return <div>{items.map((it, i) => <ToolLine key={it.e.id} item={it} live={live && i === items.length - 1} />)}</div>
  }
  const failed = items.filter(it => it.result?.label === 'error').length
  const last = items[items.length - 1]
  return (
    <div>
      <button
        onClick={() => setOpen(o => !o)}
        className="w-full flex items-start gap-1.5 text-left font-mono text-[12px] leading-5 hover:bg-gray-900/70 rounded px-1 -mx-1"
      >
        <Dot cls={live ? 'text-amber-400 animate-pulse' : failed ? 'text-red-400' : 'text-gray-500'} />
        <span className="min-w-0 truncate text-gray-300">
          {summarize(items)}
          {failed > 0 && <span className="text-red-300"> · {failed} lỗi</span>}
        </span>
        <span className="ml-auto shrink-0 text-gray-600 pl-2">{open ? '▾' : `▸ ${items.length}`}</span>
      </button>
      {open ? (
        <div className="ml-[7px] pl-2.5 border-l border-gray-800">
          {items.map((it, i) => <ToolLine key={it.e.id} item={it} live={live && i === items.length - 1} />)}
        </div>
      ) : live ? (
        <div className="ml-[7px] pl-2.5 border-l border-gray-800"><ToolLine item={last} live /></div>
      ) : null}
    </div>
  )
}

function Entry({ e, followUp }: { e: TaskLogEntry; followUp?: FollowUp }) {
  const [open, setOpen] = useState(false)
  switch (e.kind) {
    case 'status': {
      if (e.label === 'start') {
        return (
          <div className="flex items-center gap-2 pt-2 text-[11px] text-gray-500">
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
          <span className={`px-1.5 rounded ring-1 ${l.cls}`}>■ {l.text}</span>
          {e.content && <span className="text-gray-500 truncate" title={e.content}>{e.content}</span>}
        </div>
      )
    }
    case 'prompt':
      return (
        <div className="font-mono text-[12px] leading-5">
          <button onClick={() => setOpen(o => !o)} className="w-full flex items-start gap-1.5 text-left hover:bg-gray-900/70 rounded px-1 -mx-1">
            <span className="w-3 shrink-0 text-center text-sky-400">›</span>
            <span className="min-w-0 truncate">
              <span className="text-sky-300">Brief gửi agent</span>
              <span className="text-gray-500"> · {((e.content ?? '').length / 1000).toFixed(1)}k ký tự · bấm để xem</span>
            </span>
          </button>
          {open && <pre className="ml-[18px] mt-1 text-[11px] text-gray-300 bg-gray-900 rounded p-2 ring-1 ring-sky-500/20 whitespace-pre-wrap break-words max-h-96 overflow-auto">{e.content}</pre>}
        </div>
      )
    case 'thinking':
      return (
        <div className="text-[12px] leading-5">
          <button onClick={() => setOpen(o => !o)} className="w-full flex items-start gap-1.5 text-left hover:bg-gray-900/70 rounded px-1 -mx-1">
            <span className="w-3 shrink-0 text-center text-violet-400/80">✻</span>
            <span className="min-w-0 truncate italic text-gray-500">
              Suy nghĩ{!open && <span className="text-gray-600"> · {(e.content ?? '').trim().replace(/\s+/g, ' ')}</span>}
            </span>
          </button>
          {open && <div className="ml-[18px] text-xs text-gray-400 italic whitespace-pre-wrap">{e.content}</div>}
        </div>
      )
    case 'user': {
      const st = followUp ? FOLLOWUP_LABEL[followUp.state] : undefined
      return (
        <div className="flex justify-end pt-1">
          <div className="max-w-[85%]">
            <div className="rounded-lg rounded-br-sm bg-sky-600/20 ring-1 ring-sky-500/30 px-3 py-1.5 text-sm text-gray-100 whitespace-pre-wrap break-words">
              {e.content}
            </div>
            <div className="mt-0.5 text-right text-[10px] text-gray-600">
              bạn · {clock(e.created_at)}
              {followUp?.mode === 'interrupt' && <span className="ml-1 text-amber-300">· ■ chỉ hướng</span>}
              {st && <span className={`ml-1 ${st.cls}`}>· {st.text}</span>}
            </div>
          </div>
        </div>
      )
    }
    case 'text':
    default:
      return (
        <div className="flex items-start gap-1.5">
          <Dot cls="text-gray-200" />
          <div className="min-w-0 flex-1 text-sm text-gray-200 [&_.prose]:leading-relaxed">
            <MessageMarkdown>{e.content ?? ''}</MessageMarkdown>
          </div>
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
    return { label: 'Xếp hàng', hint: 'Agent đang chạy. Enter: xếp hàng cho lượt kế · ■ Dừng & chỉ hướng: cắt lượt này, agent làm tiếp ngay trong cùng phiên theo tin của bạn.' }
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

  const send = async (interrupt = false) => {
    const text = draft.trim()
    if (!text || sending || mode.disabled) return
    setSending(true)
    setErr(null)
    try {
      await call('tasks.followup', { id: task.id, message: text, interrupt })
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
        className="flex-1 min-h-0 overflow-y-auto px-4 py-3 space-y-1.5"
      >
        {!loaded && !err && <div className="text-sm text-gray-500">Đang tải phiên…</div>}
        {loaded && entries.length === 0 && (
          <div className="text-sm text-gray-500 py-8 text-center">
            {task.state === 'submitted'
              ? 'Chưa có agent nào nhận task này. Khi nó chạy, mọi bước sẽ hiện ở đây.'
              : 'Chưa có gì được ghi lại cho task này.'}
          </div>
        )}
        {shown.map((r, i) => r.type === 'tools'
          ? <ToolGroup key={`g${r.key}`} items={r.items} live={running && i === shown.length - 1} />
          : <Entry key={r.e.id} e={r.e} followUp={r.e.kind === 'user' && r.e.run_id ? byID.get(r.e.run_id) : undefined} />)}
        {running && (
          <div className="flex items-center gap-1.5 text-[12px] text-amber-300/90 pt-1">
            <span className="w-3 text-center animate-spin [animation-duration:2.5s]">✻</span>
            <span>Đang làm…</span>
            {lastTool && <span className="text-gray-600">· {entries.filter(e => e.kind === 'tool').length} tool call</span>}
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
                    void send(false)
                  }
                }}
                disabled={mode.disabled || sending}
                rows={2}
                placeholder={mode.disabled ? '' : 'Nhắn cho agent trong phiên này…  (Enter gửi · Shift+Enter xuống dòng)'}
                className="w-full bg-transparent px-3 pt-2 text-sm text-gray-100 placeholder-gray-600 outline-none resize-none max-h-48"
              />
              <div className="flex items-center gap-2 px-2 pb-2">
                <span className="text-[11px] text-gray-500 leading-snug flex-1">{mode.hint}</span>
                {running && task.state === 'working' && (
                  <button
                    onClick={() => send(true)}
                    disabled={sending || !draft.trim()}
                    title="Cắt lượt đang chạy; agent làm tiếp ngay trong cùng phiên, bắt đầu từ tin của bạn (như Esc rồi gõ trong Claude Code)"
                    className="shrink-0 px-3 py-1.5 text-xs rounded-md ring-1 ring-amber-500/50 bg-amber-500/10 text-amber-200 font-medium hover:bg-amber-500/20 disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    ■ Dừng & chỉ hướng
                  </button>
                )}
                <button
                  onClick={() => send(false)}
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
