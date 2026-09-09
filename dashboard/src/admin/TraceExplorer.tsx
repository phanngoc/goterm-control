import { useEffect, useState } from 'react'
import type { Run, TraceSummary } from './types'
import { ago, clock, ms, runColor, tokens, truncate } from './format'

type Call = (method: string, params?: any) => Promise<any>

function StatusDot({ status }: { status: string }) {
  const cls =
    status === 'error' ? 'bg-red-400'
    : status === 'pending' ? 'bg-amber-400 animate-pulse'
    : 'bg-emerald-400'
  return <span className={`inline-block h-2 w-2 rounded-full ${cls}`} title={status} />
}

function TraceRow({ t, active, onClick }: { t: TraceSummary; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={`w-full text-left px-3 py-2.5 border-b border-gray-800/70 transition-colors ${
        active ? 'bg-gray-800' : 'hover:bg-gray-900'
      }`}
    >
      <div className="flex items-center gap-2">
        <StatusDot status={t.status} />
        <span className="text-xs font-mono text-gray-500 shrink-0">{clock(t.started_at)}</span>
        <span className="text-[11px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-400 shrink-0">
          {t.agent_id}
        </span>
        <span className="text-sm text-gray-200 truncate flex-1">{t.name}</span>
        <span className="text-xs font-mono text-gray-400 tabular-nums shrink-0">{ms(t.duration_ms)}</span>
      </div>
      {t.inputs && (
        <div className="mt-1 text-xs text-gray-500 truncate pl-4">{truncate(t.inputs, 110)}</div>
      )}
      <div className="mt-1 flex items-center gap-3 pl-4 text-[11px] text-gray-600">
        <span>{t.span_count} spans</span>
        <span>{t.tool_count} tools</span>
        <span>{tokens(t.total_tokens)} tok</span>
        {t.error_count > 0 && <span className="text-red-400">{t.error_count} errors</span>}
      </div>
    </button>
  )
}

// A tool's payload reaches the page as a JSON *string*, so rendering it raw
// shows the escapes rather than the content: `&&` arrives as &&,
// quotes as \", and a heredoc as one line of literal \n. Parsing it back is most
// of the readability win; lifting the one field that matters out of the wrapper
// is the rest — for a Bash span that field is the command someone actually ran.
const PRIMARY_FIELD: Record<string, string> = {
  Bash: 'command',
  PowerShell: 'command',
  run_shell: 'command',
  Read: 'file_path',
  Write: 'file_path',
  Edit: 'file_path',
  NotebookEdit: 'notebook_path',
  Grep: 'pattern',
  Glob: 'pattern',
  WebFetch: 'url',
  browse_url: 'url',
  browser_navigate: 'url',
  Task: 'prompt',
}

// Tried in order when the span name is not one we know — the shapes above
// cover the tools that actually appear, and this catches the rest.
const FALLBACK_FIELDS = [
  'command', 'script', 'prompt', 'file_path', 'pattern', 'url', 'query',
  // Result-side shapes. PRIMARY_FIELD names the field a *call* is about, so the
  // output payload is matched by these instead.
  'stdout', 'output', 'result', 'content', 'text',
]

type Decoded = { primary?: { key: string; text: string }; rest: [string, unknown][] }

function decodePayload(body: string, name?: string): Decoded | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(body)
  } catch {
    return null // not JSON — the caller renders it as it arrived
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return null

  const obj = parsed as Record<string, unknown>
  const wanted = (name && PRIMARY_FIELD[name]) || FALLBACK_FIELDS.find(f => typeof obj[f] === 'string')

  let primary: Decoded['primary']
  if (wanted && typeof obj[wanted] === 'string') {
    primary = { key: wanted, text: obj[wanted] as string }
  }
  return { primary, rest: Object.entries(obj).filter(([k]) => k !== primary?.key) }
}

function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(
          () => { setDone(true); setTimeout(() => setDone(false), 1200) },
          () => {},
        )
      }}
      className="text-[11px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-400 hover:text-gray-200 hover:bg-gray-800"
      title="Copy so you can run it yourself"
    >
      {done ? 'copied' : 'copy'}
    </button>
  )
}

function Payload({ label, body, name }: { label: string; body?: string; name?: string }) {
  if (!body) return null
  const decoded = decodePayload(body, name)

  // Unparseable, or an object with nothing worth lifting out: show it whole,
  // but re-serialised so at least the escapes are resolved.
  if (!decoded) {
    let text = body
    try {
      text = JSON.stringify(JSON.parse(body), null, 2)
    } catch {
      /* genuinely not JSON; the raw string is the best we have */
    }
    return (
      <div className="mt-2">
        <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-1">{label}</div>
        <pre className="text-xs text-gray-300 bg-gray-950 rounded p-2 ring-1 ring-gray-800 whitespace-pre-wrap break-words max-h-64 overflow-y-auto">
          {text}
        </pre>
      </div>
    )
  }

  return (
    <div className="mt-2">
      <div className="flex items-center gap-2 mb-1">
        <span className="text-[11px] uppercase tracking-wider text-gray-600">{label}</span>
        {decoded.primary && (
          <>
            <span className="text-[11px] font-mono text-gray-500">{decoded.primary.key}</span>
            <CopyButton text={decoded.primary.text} />
          </>
        )}
      </div>

      {decoded.primary && (
        <pre className="text-xs text-gray-200 bg-gray-950 rounded p-2 ring-1 ring-gray-800 whitespace-pre-wrap break-words max-h-80 overflow-y-auto font-mono">
          {decoded.primary.text}
        </pre>
      )}

      {/* Everything else as one line each: a description or a timeout is worth
          seeing, but it should not compete with the command for attention. */}
      {decoded.rest.length > 0 && (
        <dl className="mt-1.5 space-y-0.5">
          {decoded.rest.map(([k, v]) => (
            <div key={k} className="flex gap-2 text-[11px]">
              <dt className="font-mono text-gray-600 shrink-0">{k}</dt>
              <dd className="text-gray-400 break-words min-w-0">
                {typeof v === 'string' ? v : JSON.stringify(v)}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
}

// summarise is the one line a collapsed span gets. Without it a trace of twelve
// Bash calls renders as twelve rows saying "Bash", which tells you something ran
// but never what — the difference this closes.
//
// description wins when the caller supplied one: it is a human summary of the
// command's intent, which is what you want at a glance. Otherwise the command
// itself, flattened, because a heredoc's newlines would otherwise break the row.
function summarise(run: Run): string | null {
  if (!run.inputs) return null
  const d = decodePayload(run.inputs, run.name)
  if (!d) return null

  const desc = d.rest.find(([k]) => k === 'description')?.[1]
  const text = typeof desc === 'string' && desc.trim() ? desc : d.primary?.text
  if (!text) return null

  const flat = text.replace(/\s+/g, ' ').trim()
  return flat || null
}

function WaterfallRow({ run, t0, span, expanded, onToggle }: {
  run: Run; t0: number; span: number; expanded: boolean; onToggle: () => void
}) {
  const color = runColor(run.run_type)
  const summary = summarise(run)
  const start = new Date(run.started_at).getTime()
  // A pending run has no end yet; show it running to "now" rather than as a
  // zero-width sliver that looks like it never happened.
  const dur = run.duration_ms > 0
    ? run.duration_ms
    : run.status === 'pending' ? Math.max(0, Date.now() - start) : 0

  const leftPct = span > 0 ? ((start - t0) / span) * 100 : 0
  const widthPct = span > 0 ? Math.max((dur / span) * 100, 0.6) : 100

  return (
    <div className="border-b border-gray-800/50">
      <button onClick={onToggle} className="w-full text-left px-3 py-1.5 hover:bg-gray-900/70 transition-colors">
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1.5 shrink-0" style={{ paddingLeft: run.depth * 14 }}>
            <StatusDot status={run.status} />
            <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 ${color.chip}`}>{run.run_type}</span>
            <span className="text-xs text-gray-200">{run.name}</span>
          </div>

          <div className="flex-1 min-w-[80px] h-4 relative">
            <div
              className={`absolute top-1 h-2 rounded-sm ${run.status === 'error' ? 'bg-red-500' : color.bar} ${
                run.status === 'pending' ? 'animate-pulse' : ''
              }`}
              style={{ left: `${leftPct}%`, width: `${widthPct}%` }}
            />
          </div>

          <span className="text-xs font-mono text-gray-400 tabular-nums w-16 text-right shrink-0">
            {ms(dur)}
          </span>
          <span className="text-[11px] font-mono text-gray-600 tabular-nums w-14 text-right shrink-0">
            {run.input_tokens + run.output_tokens > 0 ? tokens(run.input_tokens + run.output_tokens) : ''}
          </span>
        </div>

        {/* Second line rather than a column, so the waterfall bar keeps its
            width — the same shape the trace list already uses. */}
        {summary && !expanded && (
          <div
            className="mt-0.5 text-[11px] text-gray-500 truncate font-mono"
            style={{ paddingLeft: run.depth * 14 + 20 }}
            title={summary}
          >
            {summary}
          </div>
        )}
      </button>

      {expanded && (
        <div className="px-3 pb-3" style={{ paddingLeft: run.depth * 14 + 12 }}>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-gray-500">
            <span>started {clock(run.started_at)}</span>
            {run.model && <span>model {run.model}</span>}
            {run.input_tokens > 0 && <span>in {run.input_tokens}</span>}
            {run.output_tokens > 0 && <span>out {run.output_tokens}</span>}
            <span className="font-mono">{run.id.slice(0, 8)}</span>
          </div>
          {run.error && (
            <div className="mt-2 text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2 whitespace-pre-wrap">
              {run.error}
            </div>
          )}
          <Payload label="input" body={run.inputs} name={run.name} />
          {/* No name for the output: PRIMARY_FIELD maps a tool to the field its
              call is about, which a result does not have. */}
          <Payload label="output" body={run.outputs} />
        </div>
      )}
    </div>
  )
}

function Waterfall({ runs }: { runs: Run[] }) {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  if (runs.length === 0) return <div className="p-6 text-sm text-gray-500">No spans in this trace.</div>

  const t0 = Math.min(...runs.map(r => new Date(r.started_at).getTime()))
  const end = Math.max(
    ...runs.map(r => {
      const s = new Date(r.started_at).getTime()
      return r.duration_ms > 0 ? s + r.duration_ms : s
    })
  )
  const span = Math.max(end - t0, 1)
  const root = runs[0]

  return (
    <div className="h-full flex flex-col">
      <div className="px-4 py-3 border-b border-gray-800 bg-gray-900/60">
        <div className="flex items-center gap-2">
          <StatusDot status={root.status} />
          <span className="text-sm font-medium text-gray-100">{root.name}</span>
          <span className="text-[11px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-400">
            {root.agent_id}
          </span>
          <span className="ml-auto text-xs font-mono text-gray-400">{ms(root.duration_ms)}</span>
        </div>
        <div className="mt-1 flex items-center gap-3 text-[11px] text-gray-500">
          <span>{runs.length} spans</span>
          <span>{ago(root.started_at)}</span>
          {root.session_id && <span className="font-mono">{root.session_id}</span>}
          <span className="font-mono text-gray-600">{root.trace_id.slice(0, 8)}</span>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto">
        {runs.map(r => (
          <WaterfallRow
            key={r.id}
            run={r}
            t0={t0}
            span={span}
            expanded={!!open[r.id]}
            onToggle={() => setOpen(o => ({ ...o, [r.id]: !o[r.id] }))}
          />
        ))}
      </div>
    </div>
  )
}

export default function TraceExplorer({ call, agents }: { call: Call; agents: string[] }) {
  const [traces, setTraces] = useState<TraceSummary[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [runs, setRuns] = useState<Run[]>([])
  const [agentFilter, setAgentFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [search, setSearch] = useState('')
  const [live, setLive] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const rows = await call('traces.list', {
          agent_id: agentFilter || undefined,
          status: statusFilter || undefined,
          search: search || undefined,
          limit: 100,
        })
        if (!cancelled) { setTraces(rows || []); setError(null) }
      } catch (e: any) {
        if (!cancelled) setError(String(e?.message ?? e))
      }
    }
    load()
    if (!live) return () => { cancelled = true }
    const id = setInterval(load, 4000)
    return () => { cancelled = true; clearInterval(id) }
  }, [call, agentFilter, statusFilter, search, live])

  useEffect(() => {
    if (!selected) { setRuns([]); return }
    let cancelled = false
    const load = async () => {
      try {
        const rows = await call('traces.get', { trace_id: selected })
        if (!cancelled) setRuns(rows || [])
      } catch { /* the list already surfaces connection problems */ }
    }
    load()
    // A selected trace may still be running; keep its waterfall current.
    const id = setInterval(load, 3000)
    return () => { cancelled = true; clearInterval(id) }
  }, [call, selected])

  return (
    <div className="h-full flex">
      <div className="w-[420px] shrink-0 border-r border-gray-800 flex flex-col">
        <div className="p-3 space-y-2 border-b border-gray-800 bg-gray-900/40">
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search turns…"
            className="w-full px-2.5 py-1.5 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
          />
          <div className="flex items-center gap-2">
            <select
              value={agentFilter}
              onChange={e => setAgentFilter(e.target.value)}
              className="flex-1 px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
            >
              <option value="">all agents</option>
              {agents.map(a => <option key={a} value={a}>{a}</option>)}
            </select>
            <select
              value={statusFilter}
              onChange={e => setStatusFilter(e.target.value)}
              className="flex-1 px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
            >
              <option value="">any status</option>
              <option value="success">success</option>
              <option value="error">error</option>
              <option value="pending">running</option>
            </select>
            <button
              onClick={() => setLive(v => !v)}
              title="Auto-refresh every 4s"
              className={`px-2 py-1 text-xs rounded ring-1 transition-colors ${
                live ? 'ring-emerald-500/40 bg-emerald-500/10 text-emerald-300' : 'ring-gray-800 text-gray-500'
              }`}
            >
              {live ? '● live' : 'paused'}
            </button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto">
          {error && <div className="p-4 text-xs text-red-300">{error}</div>}
          {!error && traces.length === 0 && (
            <div className="p-4 text-sm text-gray-500">
              No traces yet. Send the agent a message and its turn will show up here.
            </div>
          )}
          {traces.map(t => (
            <TraceRow
              key={t.trace_id}
              t={t}
              active={t.trace_id === selected}
              onClick={() => setSelected(t.trace_id)}
            />
          ))}
        </div>
      </div>

      <div className="flex-1 min-w-0">
        {selected ? (
          <Waterfall runs={runs} />
        ) : (
          <div className="h-full flex items-center justify-center text-sm text-gray-600">
            Select a turn to see its waterfall
          </div>
        )}
      </div>
    </div>
  )
}
