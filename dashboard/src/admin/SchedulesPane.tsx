import { useCallback, useEffect, useState } from 'react'
import type { AgentPayload, CommandPayload, HeartbeatPayload, Schedule, ScheduleKind, ScheduleView } from './types'
import { ago, isZeroTime, rel, truncate } from './format'

type Call = (method: string, params?: any) => Promise<any>

// The gateway stores "never again" (a one-shot that has fired) as this far
// future instant so next_run_at can stay NOT NULL and sort last.
const NEVER = '9999-'

const STATUS_STYLE: Record<string, string> = {
  ok:      'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  pending: 'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  failed:  'bg-red-500/15 text-red-300 ring-red-500/30',
  skipped: 'bg-gray-500/15 text-gray-400 ring-gray-500/30',
}
function statusStyle(s?: string) {
  return STATUS_STYLE[s ?? ''] ?? 'bg-gray-500/15 text-gray-500 ring-gray-500/30'
}

const PAYLOAD_STYLE: Record<string, string> = {
  agent:     'bg-fuchsia-500/15 text-fuchsia-300 ring-fuchsia-500/30',
  command:   'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  heartbeat: 'bg-rose-500/15 text-rose-300 ring-rose-500/30',
}

// payloadText is the one-line summary of what a firing does.
function payloadText(s: Schedule): string {
  switch (s.payload_kind) {
    case 'agent': {
      const p = s.payload as AgentPayload
      return 'task: ' + p.title + (p.to ? ` → ${p.to}` : '') + (p.quiet ? ' (quiet)' : '')
    }
    case 'command': {
      const p = s.payload as CommandPayload
      return 'sh: ' + p.cmd
    }
    case 'heartbeat': {
      const p = s.payload as HeartbeatPayload
      return 'look at the scratchpad' + (p.active_hours ? ` · ${p.active_hours}` : '')
    }
  }
  return s.payload_kind
}

// nextText: what the NEXT column says. Order matters — a disabled one-shot
// that already fired is "done", not "disabled".
function nextText(s: Schedule): string {
  const done = s.next_run_at.startsWith(NEVER)
  if (done) return s.enabled ? 'never' : 'done'
  if (!s.enabled) return 'disabled'
  const t = new Date(s.next_run_at)
  return `${t.toLocaleString([], { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })} (${rel(s.next_run_at)})`
}

function duration(startISO: string, endISO?: string): string {
  if (isZeroTime(endISO)) return '…'
  const secs = Math.max(0, Math.round((new Date(endISO!).getTime() - new Date(startISO).getTime()) / 1000))
  return secs >= 60 ? `${Math.round(secs / 60)}m` : `${secs}s`
}

function ScheduleDrawer({ call, id, onClose, onChanged }: {
  call: Call; id: string; onClose: () => void; onChanged: () => void
}) {
  const [view, setView] = useState<ScheduleView | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    call('schedules.get', { id, limit: 30 })
      .then((v: ScheduleView) => { setView(v); setErr(null) })
      .catch((e: any) => setErr(String(e?.message ?? e)))
  }, [call, id])

  useEffect(() => {
    load()
    // A pending run settles when its task finishes; that is worth seeing
    // without closing and reopening the drawer.
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  const act = async (method: string, params: any, closeAfter = false) => {
    setBusy(true)
    try {
      await call(method, params)
      onChanged()
      if (closeAfter) onClose(); else load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const s = view?.schedule
  const done = s?.next_run_at.startsWith(NEVER)

  return (
    <div className="fixed inset-0 z-40 flex justify-end bg-black/50" onClick={onClose}>
      <div className="w-[600px] max-w-full h-full bg-gray-950 ring-1 ring-gray-800 flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="px-4 py-3 border-b border-gray-800 flex items-center gap-2">
          <span className="text-sm font-medium text-gray-100">Schedule</span>
          <span className="font-mono text-xs text-gray-500 truncate">{s?.name ?? id}</span>
          <button onClick={onClose} className="ml-auto text-gray-500 hover:text-gray-300 text-sm">✕</button>
        </div>

        <div className="flex-1 overflow-y-auto p-4 space-y-4">
          {err && <div className="text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2">{err}</div>}
          {!view && !err && <div className="text-sm text-gray-500">Loading…</div>}

          {s && (
            <>
              <div>
                <div className="flex items-center gap-2 flex-wrap">
                  <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 ${PAYLOAD_STYLE[s.payload_kind] ?? ''}`}>{s.payload_kind}</span>
                  <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 ${s.enabled ? 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30' : 'bg-gray-500/15 text-gray-400 ring-gray-500/30'}`}>
                    {s.enabled ? 'enabled' : 'disabled'}
                  </span>
                  {s.system && <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 bg-gray-500/15 text-gray-300 ring-gray-500/30" title="Owned by the gateway; change it in config.yaml">system</span>}
                  {s.skip_missed && <span className="text-[10px] text-gray-500" title="After downtime it re-arms instead of running the missed firing">skip missed</span>}
                </div>
                <h3 className="mt-2 text-base text-gray-100">{s.name}</h3>
                <div className="mt-1 text-sm text-gray-300 font-mono">{view!.when}</div>
                <div className="mt-2 text-sm text-gray-300">{payloadText(s)}</div>
                {s.payload_kind === 'agent' && (s.payload as AgentPayload).body && (
                  <pre className="mt-2 text-xs text-gray-300 bg-gray-900 rounded p-3 ring-1 ring-gray-800 whitespace-pre-wrap break-words">
                    {(s.payload as AgentPayload).body}
                  </pre>
                )}
                {s.payload_kind === 'command' && (
                  <div className="mt-1 text-xs text-gray-500">
                    {(s.payload as CommandPayload).cwd && <>cwd {(s.payload as CommandPayload).cwd} · </>}
                    timeout {(s.payload as CommandPayload).timeout_s || 'gateway default'}{(s.payload as CommandPayload).timeout_s ? 's' : ''}
                  </div>
                )}
              </div>

              <dl className="grid grid-cols-2 gap-2 text-xs">
                <div><dt className="text-gray-600">next</dt><dd className="text-gray-300">{nextText(s)}</dd></div>
                <div><dt className="text-gray-600">last</dt><dd className="text-gray-300">{!isZeroTime(s.last_run_at) ? <>{s.last_status} · {ago(s.last_run_at)}</> : '—'}</dd></div>
                <div><dt className="text-gray-600">owner</dt><dd className="font-mono text-gray-300">{s.owner_agent || 'any gateway'}</dd></div>
                <div><dt className="text-gray-600">created by</dt><dd className="font-mono text-gray-300">{s.created_by}</dd></div>
                <div><dt className="text-gray-600">time zone</dt><dd className="font-mono text-gray-300">{s.tz}</dd></div>
                <div>
                  <dt className="text-gray-600">failures in a row</dt>
                  <dd className={s.consecutive_failures > 0 ? 'text-red-300' : 'text-gray-300'}>
                    {s.consecutive_failures}{s.consecutive_failures > 0 && <span className="text-gray-500"> · turns itself off at 10</span>}
                  </dd>
                </div>
              </dl>

              <div>
                <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-2">Runs</div>
                {view!.runs.length === 0 && <div className="text-xs text-gray-600">none yet</div>}
                <ol className="space-y-2">
                  {view!.runs.map(r => (
                    <li key={r.id} className="text-xs">
                      <div className="flex items-baseline gap-2">
                        <span className="text-gray-600 font-mono shrink-0">{ago(r.started_at)}</span>
                        <span className={`px-1.5 rounded ring-1 shrink-0 ${statusStyle(r.status)}`}>{r.status}</span>
                        <span className="text-gray-500 shrink-0">{duration(r.started_at, r.ended_at)}</span>
                        {r.task_id
                          ? <span className="font-mono text-gray-400 truncate" title={r.task_id}>task {r.task_id.slice(0, 10)}…</span>
                          : <span className="text-gray-500">exit {r.exit_code}</span>}
                      </div>
                      {r.output && (
                        <pre className="mt-1 ml-1 text-[11px] text-gray-400 bg-gray-900 rounded p-2 ring-1 ring-gray-800 whitespace-pre-wrap break-words max-h-40 overflow-y-auto">
                          {truncate(r.output, 1500)}
                        </pre>
                      )}
                    </li>
                  ))}
                </ol>
              </div>
            </>
          )}
        </div>

        {s && (
          <div className="px-4 py-3 border-t border-gray-800 flex gap-2 flex-wrap">
            {s.enabled && !done && (
              <button onClick={() => act('schedules.run', { id: s.id })} disabled={busy}
                className="px-3 py-1.5 text-sm rounded ring-1 ring-sky-500/40 bg-sky-500/10 text-sky-300 hover:bg-sky-500/20 disabled:opacity-50"
                title="Fire on the next tick (within a second here), then resume the cadence">
                Run now
              </button>
            )}
            {!done && (
              <button onClick={() => act('schedules.toggle', { id: s.id, enabled: !s.enabled })} disabled={busy}
                className="px-3 py-1.5 text-sm rounded ring-1 ring-gray-600 bg-gray-800 text-gray-200 hover:bg-gray-700 disabled:opacity-50"
                title={s.enabled ? 'Keep the row and its history; nothing fires' : 'Next run computed from now; failure streak reset'}>
                {s.enabled ? 'Disable' : 'Enable'}
              </button>
            )}
            {!s.system && (
              <button onClick={() => { if (confirm(`Remove schedule "${s.name}" and its run history?`)) act('schedules.delete', { id: s.id }, true) }} disabled={busy}
                className="ml-auto px-3 py-1.5 text-sm rounded ring-1 ring-red-500/40 bg-red-500/10 text-red-300 hover:bg-red-500/20 disabled:opacity-50">
                Remove
              </button>
            )}
            {s.system && <span className="ml-auto self-center text-[11px] text-gray-600">heartbeat rows follow config.yaml</span>}
          </div>
        )}
      </div>
    </div>
  )
}

const SPEC_HINT: Record<ScheduleKind, { placeholder: string; help: string }> = {
  cron:  { placeholder: '0 8 * * 1-5', help: '5 fields (min hour dom month dow) or @daily / @hourly. Day-of-month AND day-of-week set → fires when EITHER matches.' },
  every: { placeholder: '30m', help: 'Go duration: 10m, 1h30m. Minimum 1m. Counts from each firing.' },
  at:    { placeholder: '2026-09-07T09:00', help: 'One-shot, read in the time zone below. Switches itself off after it runs.' },
}

function NewScheduleForm({ call, agents, onCreated }: { call: Call; agents: string[]; onCreated: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [kind, setKind] = useState<ScheduleKind>('cron')
  const [spec, setSpec] = useState('')
  const [tz, setTz] = useState('')
  const [payloadKind, setPayloadKind] = useState<'agent' | 'command'>('agent')
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [to, setTo] = useState('')
  const [quiet, setQuiet] = useState(false)
  const [cmd, setCmd] = useState('')
  const [cwd, setCwd] = useState('')
  const [timeout, setTimeoutS] = useState('')
  const [owner, setOwner] = useState('')
  const [skipMissed, setSkipMissed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const ready = name.trim() && spec.trim() && (payloadKind === 'agent' ? title.trim() : cmd.trim())

  const create = async () => {
    if (!ready) return
    setBusy(true)
    try {
      const payload = payloadKind === 'agent'
        ? { title: title.trim(), body: body.trim() || undefined, to: to || undefined, quiet: quiet || undefined }
        : { cmd: cmd.trim(), cwd: cwd.trim() || undefined, timeout_s: timeout ? Number(timeout) : undefined }
      await call('schedules.create', {
        name: name.trim(), kind, spec: spec.trim(), tz: tz.trim() || undefined,
        payload_kind: payloadKind, payload,
        owner_agent: owner || undefined, skip_missed: skipMissed || undefined,
      })
      setName(''); setSpec(''); setTitle(''); setBody(''); setCmd(''); setCwd(''); setTimeoutS(''); setQuiet(false); setSkipMissed(false)
      setErr(null)
      setOpen(false)
      onCreated()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const field = 'px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600'

  if (!open) {
    return (
      <div className="p-4 border-b border-gray-800 bg-gray-900/40 flex items-center gap-3">
        <button onClick={() => setOpen(true)}
          className="px-4 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white">
          New schedule
        </button>
        <span className="text-xs text-gray-500">
          A schedule creates a task or runs a command at the appointed time. The gateway that has
          <code className="mx-1 font-mono text-gray-400">schedules.enabled</code> fires it; results go to Telegram.
        </span>
      </div>
    )
  }

  return (
    <div className="p-4 border-b border-gray-800 bg-gray-900/40 space-y-3">
      <div className="flex gap-2">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="name (unique, e.g. morning-briefing)" className={`flex-1 ${field}`} />
        <select value={owner} onChange={e => setOwner(e.target.value)} className={field} title="Which gateway fires it">
          <option value="">any gateway</option>
          {agents.map(a => <option key={a} value={a}>only {a}</option>)}
        </select>
      </div>

      <div className="flex gap-2 items-start">
        <div className="flex rounded ring-1 ring-gray-800 overflow-hidden shrink-0">
          {(['cron', 'every', 'at'] as ScheduleKind[]).map(k => (
            <button key={k} onClick={() => setKind(k)}
              className={`px-3 py-2 text-sm ${kind === k ? 'bg-gray-700 text-white' : 'bg-gray-950 text-gray-400 hover:text-gray-200'}`}>
              {k}
            </button>
          ))}
        </div>
        <div className="flex-1">
          <input value={spec} onChange={e => setSpec(e.target.value)} placeholder={SPEC_HINT[kind].placeholder} className={`w-full font-mono ${field}`} />
          <div className="mt-1 text-[11px] text-gray-500">{SPEC_HINT[kind].help}</div>
        </div>
        <input value={tz} onChange={e => setTz(e.target.value)} placeholder="tz (machine's)" title="IANA zone, e.g. Asia/Ho_Chi_Minh. Empty = the gateway machine's zone." className={`w-44 ${field}`} />
      </div>

      <div className="flex gap-2 items-start">
        <div className="flex rounded ring-1 ring-gray-800 overflow-hidden shrink-0">
          {(['agent', 'command'] as const).map(k => (
            <button key={k} onClick={() => setPayloadKind(k)}
              className={`px-3 py-2 text-sm ${payloadKind === k ? 'bg-gray-700 text-white' : 'bg-gray-950 text-gray-400 hover:text-gray-200'}`}>
              {k === 'agent' ? 'agent task' : 'shell command'}
            </button>
          ))}
        </div>
        {payloadKind === 'agent' ? (
          <div className="flex-1 space-y-2">
            <div className="flex gap-2">
              <input value={title} onChange={e => setTitle(e.target.value)} placeholder="task title — what the agent should do" className={`flex-1 ${field}`} />
              <select value={to} onChange={e => setTo(e.target.value)} className={field}>
                <option value="">any agent</option>
                {agents.map(a => <option key={a} value={a}>{a}</option>)}
              </select>
            </div>
            <textarea value={body} onChange={e => setBody(e.target.value)} rows={2} placeholder="details (optional)" className={`w-full resize-none ${field}`} />
            <label className="flex items-center gap-2 text-xs text-gray-400">
              <input type="checkbox" checked={quiet} onChange={e => setQuiet(e.target.checked)} />
              quiet — record the result, do not send it to Telegram
            </label>
          </div>
        ) : (
          <div className="flex-1 space-y-2">
            <input value={cmd} onChange={e => setCmd(e.target.value)} placeholder="df -h /" className={`w-full font-mono ${field}`} />
            <div className="flex gap-2">
              <input value={cwd} onChange={e => setCwd(e.target.value)} placeholder="cwd (optional)" className={`flex-1 font-mono ${field}`} />
              <input value={timeout} onChange={e => setTimeoutS(e.target.value.replace(/\D/g, ''))} placeholder="timeout s" className={`w-28 ${field}`} />
            </div>
            <div className="text-[11px] text-gray-500">Runs inside the gateway with no model; output is kept (8 KB). Exit ≠ 0 counts as a failure.</div>
          </div>
        )}
      </div>

      <div className="flex items-center gap-3">
        <label className="flex items-center gap-2 text-xs text-gray-400">
          <input type="checkbox" checked={skipMissed} onChange={e => setSkipMissed(e.target.checked)} />
          skip missed — after downtime, re-arm instead of running the missed firing once
        </label>
        <div className="ml-auto flex gap-2">
          <button onClick={() => { setOpen(false); setErr(null) }} className="px-3 py-2 text-sm text-gray-400 hover:text-gray-200">Cancel</button>
          <button onClick={create} disabled={!ready || busy}
            className="px-4 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed">
            Create
          </button>
        </div>
      </div>
      {err && <div className="text-xs text-red-300">{err}</div>}
    </div>
  )
}

export default function SchedulesPane({ call, agents }: { call: Call; agents: string[] }) {
  const [rows, setRows] = useState<Schedule[]>([])
  const [openID, setOpenID] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setRows((await call('schedules.list')) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => {
    load()
    const id = setInterval(load, 10_000)
    return () => clearInterval(id)
  }, [load])

  const quick = async (method: string, params: any) => {
    try {
      await call(method, params)
      load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  return (
    <div className="h-full flex flex-col">
      <NewScheduleForm call={call} agents={agents} onCreated={load} />
      {err && <div className="px-4 pt-3 text-xs text-red-300">{err}</div>}

      <div className="flex-1 overflow-y-auto p-4">
        {rows.length === 0 ? (
          <div className="rounded-lg ring-1 ring-dashed ring-gray-800 p-6 text-sm text-gray-500">
            No schedules yet. Create one above, or from a shell:
            <pre className="mt-2 text-xs text-gray-400 font-mono">bomclaw schedule add --name disk --every 10m --command "df -h /"</pre>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-[11px] uppercase tracking-wider text-gray-500 text-left">
                <th className="pb-2 pr-3 font-medium">name</th>
                <th className="pb-2 pr-3 font-medium">when</th>
                <th className="pb-2 pr-3 font-medium">next</th>
                <th className="pb-2 pr-3 font-medium">last</th>
                <th className="pb-2 pr-3 font-medium">does</th>
                <th className="pb-2 pr-3 font-medium">owner</th>
                <th className="pb-2 font-medium"></th>
              </tr>
            </thead>
            <tbody className="align-top">
              {rows.map(s => {
                const done = s.next_run_at.startsWith(NEVER)
                return (
                  <tr key={s.id} className={`border-t border-gray-800/70 hover:bg-gray-900/60 ${s.enabled ? '' : 'opacity-60'}`}>
                    <td className="py-2 pr-3">
                      <button onClick={() => setOpenID(s.id)} className="text-left text-gray-100 hover:underline">{s.name}</button>
                      <div className="mt-0.5 flex gap-1">
                        <span className={`text-[10px] px-1.5 rounded ring-1 ${PAYLOAD_STYLE[s.payload_kind] ?? ''}`}>{s.payload_kind}</span>
                        {s.system && <span className="text-[10px] px-1.5 rounded ring-1 bg-gray-500/15 text-gray-300 ring-gray-500/30">system</span>}
                      </div>
                    </td>
                    <td className="py-2 pr-3 font-mono text-xs text-gray-300 whitespace-nowrap">{s.when}</td>
                    <td className="py-2 pr-3 text-xs text-gray-300 whitespace-nowrap">{nextText(s)}</td>
                    <td className="py-2 pr-3 text-xs whitespace-nowrap">
                      {!isZeroTime(s.last_run_at) ? (
                        <span className="flex items-center gap-1.5">
                          <span className={`px-1.5 rounded ring-1 ${statusStyle(s.last_status)}`}>{s.last_status}</span>
                          {s.consecutive_failures > 1 && <span className="text-red-300">×{s.consecutive_failures}</span>}
                          <span className="text-gray-500">{ago(s.last_run_at)}</span>
                        </span>
                      ) : <span className="text-gray-600">—</span>}
                    </td>
                    <td className="py-2 pr-3 text-xs text-gray-300 max-w-[22rem] truncate" title={payloadText(s)}>{payloadText(s)}</td>
                    <td className="py-2 pr-3 font-mono text-xs text-gray-400">{s.owner_agent || 'any'}</td>
                    <td className="py-2 text-xs whitespace-nowrap text-right">
                      {s.enabled && !done && (
                        <button onClick={() => quick('schedules.run', { id: s.id })} className="text-sky-300 hover:text-sky-200 mr-3" title="Fire on the next tick">run</button>
                      )}
                      {!done && (
                        <button onClick={() => quick('schedules.toggle', { id: s.id, enabled: !s.enabled })} className="text-gray-400 hover:text-gray-200">
                          {s.enabled ? 'disable' : 'enable'}
                        </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>

      {openID && <ScheduleDrawer call={call} id={openID} onClose={() => setOpenID(null)} onChanged={load} />}
    </div>
  )
}
