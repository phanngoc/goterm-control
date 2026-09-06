import { useCallback, useEffect, useState } from 'react'
import type { Task, TaskDetail } from './types'
import { ago, runLivenessStyle, taskStateStyle, truncate } from './format'
import { useStore } from '../stores/store'

type Call = (method: string, params?: any) => Promise<any>

// Lanes group the seven task states into what a person actually wants to see:
// waiting, in flight, and finished.
const LANES: { key: string; label: string; states: string[] }[] = [
  { key: 'queued', label: 'Queued', states: ['submitted'] },
  { key: 'active', label: 'In flight', states: ['working', 'input-required', 'blocked'] },
  { key: 'done', label: 'Finished', states: ['completed', 'failed', 'canceled', 'rejected'] },
]

// FAIL_REASON says why the SYSTEM gave up, in words a person acts on.
const FAIL_REASON: Record<string, string> = {
  'exhausted': 'every attempt ended without a result',
  'continuations-exhausted': 'ran out of continuations — `bomclaw task resume` to grant more',
  'empty-exhausted': 'kept returning nothing',
}

function TaskCard({ task, onOpen }: { task: Task; onOpen: () => void }) {
  // A lapsed lease on a task that still has attempts WILL be reclaimed. One
  // that has none is failed by the reaper and shows up as failed — the old
  // "will be reclaimed" on an exhausted task was a lie.
  const leaseExpired = task.state === 'working' && new Date(task.lease_until).getTime() < Date.now()
  return (
    <button
      onClick={onOpen}
      className="w-full text-left rounded-lg bg-gray-900 ring-1 ring-gray-800 hover:ring-gray-700 p-3 transition-colors"
    >
      <div className="flex items-start gap-2">
        <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 shrink-0 ${taskStateStyle(task.state)}`}>
          {task.state}
        </span>
        {task.priority > 0 && (
          <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-amber-500/30 bg-amber-500/10 text-amber-300 shrink-0">
            P{task.priority}
          </span>
        )}
      </div>
      <div className="mt-2 text-sm text-gray-100 leading-snug">{truncate(task.title, 90)}</div>
      <div className="mt-2 flex items-center gap-2 text-[11px] text-gray-500">
        <span className="font-mono">{task.created_by}</span>
        <span>→</span>
        <span className="font-mono">{task.claimed_by || task.assigned_to || 'any'}</span>
        <span className="ml-auto">{ago(task.created_at)}</span>
      </div>
      {task.state === 'failed' && task.fail_reason && (
        <div className="mt-1.5 text-[11px] text-red-400">{FAIL_REASON[task.fail_reason] ?? task.fail_reason}</div>
      )}
      {task.state === 'blocked' && (
        <div className="mt-1.5 text-[11px] text-violet-300">waiting on {task.blocked_on || 'a person'}</div>
      )}
      {task.state !== 'failed' && (task.attempts > 1 || task.continuations > 0 || leaseExpired) && (
        <div className="mt-1.5 text-[11px] text-amber-400">
          {leaseExpired
            ? 'lease lapsed — will be reclaimed'
            : [
                task.continuations > 0 ? `run ${task.continuations + 1}` : null,
                task.attempts > 1 ? `attempt ${task.attempts}/${task.max_attempts}` : null,
              ].filter(Boolean).join(' · ')}
        </div>
      )}
    </button>
  )
}

function TaskDrawer({ call, id, onClose, onChanged }: {
  call: Call; id: string; onClose: () => void; onChanged: () => void
}) {
  const [detail, setDetail] = useState<TaskDetail | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    call('tasks.get', { id })
      .then((d: TaskDetail) => { if (!cancelled) setDetail(d) })
      .catch((e: any) => { if (!cancelled) setErr(String(e?.message ?? e)) })
    return () => { cancelled = true }
  }, [call, id])

  const cancel = async () => {
    setBusy(true)
    try {
      await call('tasks.cancel', { id })
      onChanged()
      onClose()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const resume = async () => {
    setBusy(true)
    try {
      await call('tasks.resume', { id, more: 5 })
      onChanged()
      onClose()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const t = detail?.task
  const open = t && !['completed', 'failed', 'canceled', 'rejected'].includes(t.state)
  const resumable = t && t.state === 'failed' && !!t.fail_reason

  return (
    <div className="fixed inset-0 z-40 flex justify-end bg-black/50" onClick={onClose}>
      <div
        className="w-[560px] max-w-full h-full bg-gray-950 ring-1 ring-gray-800 flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="px-4 py-3 border-b border-gray-800 flex items-center gap-2">
          <span className="text-sm font-medium text-gray-100">Task</span>
          <span className="font-mono text-xs text-gray-500 truncate">{id}</span>
          <button onClick={onClose} className="ml-auto text-gray-500 hover:text-gray-300 text-sm">✕</button>
        </div>

        <div className="flex-1 overflow-y-auto p-4 space-y-4">
          {err && <div className="text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2">{err}</div>}
          {!detail && !err && <div className="text-sm text-gray-500">Loading…</div>}

          {t && (
            <>
              <div>
                <div className="flex items-center gap-2">
                  <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 ${taskStateStyle(t.state)}`}>{t.state}</span>
                  <span className="text-[11px] text-gray-500">
                    attempt {t.attempts}/{t.max_attempts} · run {t.continuations + 1}/{t.max_continuations} · depth {t.depth}
                    {t.kind !== 'manual' && <> · {t.kind}</>}
                  </span>
                </div>
                {t.fail_reason && (
                  <div className="mt-1 text-xs text-red-300">{FAIL_REASON[t.fail_reason] ?? t.fail_reason}</div>
                )}
                {t.state === 'blocked' && (
                  <div className="mt-1 text-xs text-violet-300">
                    waiting on {t.blocked_on || 'a person'}
                    {t.blocked_on === 'human' && <> — answer with <code className="text-gray-400">bomclaw task answer --id {t.id} --note "…"</code></>}
                  </div>
                )}
                <h3 className="mt-2 text-base text-gray-100">{t.title}</h3>
                {t.body && (
                  <pre className="mt-2 text-xs text-gray-300 bg-gray-900 rounded p-3 ring-1 ring-gray-800 whitespace-pre-wrap break-words">
                    {t.body}
                  </pre>
                )}
              </div>

              {t.checkpoint && (
                <div>
                  <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-1">Checkpoint · fed to the next run</div>
                  <pre className="text-xs text-gray-300 bg-gray-900 rounded p-3 ring-1 ring-sky-500/20 whitespace-pre-wrap break-words">
                    {t.checkpoint}
                  </pre>
                </div>
              )}

              {t.result && (
                <div>
                  <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-1">Result</div>
                  <pre className="text-xs text-gray-300 bg-gray-900 rounded p-3 ring-1 ring-gray-800 whitespace-pre-wrap break-words">
                    {t.result}
                  </pre>
                </div>
              )}

              <dl className="grid grid-cols-2 gap-2 text-xs">
                <div><dt className="text-gray-600">created by</dt><dd className="font-mono text-gray-300">{t.created_by}</dd></div>
                <div><dt className="text-gray-600">claimed by</dt><dd className="font-mono text-gray-300">{t.claimed_by || '—'}</dd></div>
                <div><dt className="text-gray-600">assigned to</dt><dd className="font-mono text-gray-300">{t.assigned_to || 'any'}</dd></div>
                <div><dt className="text-gray-600">context</dt><dd className="font-mono text-gray-300 truncate">{t.context_id}</dd></div>
              </dl>

              {detail!.runs?.length > 0 && (
                <div>
                  <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-2">Runs</div>
                  <ol className="space-y-1.5">
                    {detail!.runs.map((r, i) => {
                      const ended = r.ended_at ? new Date(r.ended_at).getTime() : Date.now()
                      const secs = Math.max(0, Math.round((ended - new Date(r.started_at).getTime()) / 1000))
                      const dur = secs >= 60 ? `${Math.round(secs / 60)}m` : `${secs}s`
                      return (
                        <li key={r.id} className="flex items-baseline gap-2 text-xs">
                          <span className="text-gray-600 font-mono shrink-0 w-4">{i + 1}.</span>
                          <span className={`px-1.5 rounded ring-1 shrink-0 ${runLivenessStyle(r.liveness)}`}>{r.liveness}</span>
                          <span className="text-gray-500 shrink-0">{dur}</span>
                          <span className="font-mono text-gray-400 shrink-0">{r.agent_id}</span>
                          {r.note && <span className="text-gray-500 truncate">{r.note}</span>}
                        </li>
                      )
                    })}
                  </ol>
                </div>
              )}

              {detail!.events.length > 0 && (
                <div>
                  <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-2">History</div>
                  <ol className="space-y-1.5">
                    {detail!.events.map(e => (
                      <li key={e.id} className="flex items-baseline gap-2 text-xs">
                        <span className="text-gray-600 font-mono shrink-0">{ago(e.created_at)}</span>
                        <span className={`px-1.5 rounded ring-1 shrink-0 ${taskStateStyle(e.to_state)}`}>{e.to_state}</span>
                        <span className="font-mono text-gray-400 shrink-0">{e.agent_id}</span>
                        <span className="text-gray-500">{e.note}</span>
                      </li>
                    ))}
                  </ol>
                </div>
              )}
            </>
          )}
        </div>

        {(open || resumable) && (
          <div className="px-4 py-3 border-t border-gray-800 flex gap-2">
            {open && (
              <button
                onClick={cancel}
                disabled={busy}
                className="px-3 py-1.5 text-sm rounded ring-1 ring-red-500/40 bg-red-500/10 text-red-300 hover:bg-red-500/20 disabled:opacity-50"
              >
                Cancel task
              </button>
            )}
            {resumable && (
              <button
                onClick={resume}
                disabled={busy}
                className="px-3 py-1.5 text-sm rounded ring-1 ring-sky-500/40 bg-sky-500/10 text-sky-300 hover:bg-sky-500/20 disabled:opacity-50"
                title="Grant 5 more attempts/continuations and put it back in the queue"
              >
                Resume (+5)
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

export default function TaskBoard({ call, agents }: { call: Call; agents: string[] }) {
  const [tasks, setTasks] = useState<Task[]>([])
  const [openID, setOpenID] = useState<string | null>(null)
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [to, setTo] = useState('')
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setTasks((await call('tasks.list', { limit: 200 })) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => {
    load()
    const id = setInterval(load, 5000)
    return () => clearInterval(id)
  }, [load])

  // A task run starting or finishing anywhere is pushed as session.turn with
  // kind "task"; reload at once instead of waiting for the poll.
  const externalTurn = useStore(s => s.externalTurn)
  useEffect(() => {
    if (externalTurn?.kind === 'task') load()
  }, [externalTurn, load])

  const create = async () => {
    if (!title.trim()) return
    try {
      await call('tasks.create', { title, body, assigned_to: to || undefined })
      setTitle(''); setBody(''); setTo('')
      load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  return (
    <div className="h-full flex flex-col">
      <div className="p-4 border-b border-gray-800 bg-gray-900/40">
        <div className="flex gap-2">
          <input
            value={title}
            onChange={e => setTitle(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) create() }}
            placeholder="Give an agent something to do…"
            className="flex-1 px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
          />
          <select
            value={to}
            onChange={e => setTo(e.target.value)}
            className="px-2 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
          >
            <option value="">any agent</option>
            {agents.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
          <button
            onClick={create}
            disabled={!title.trim()}
            className="px-4 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed"
          >
            Create
          </button>
        </div>
        <textarea
          value={body}
          onChange={e => setBody(e.target.value)}
          placeholder="Details (optional)"
          rows={2}
          className="mt-2 w-full px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600 resize-none"
        />
        {err && <div className="mt-2 text-xs text-red-300">{err}</div>}
      </div>

      <div className="flex-1 overflow-y-auto p-4">
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
          {LANES.map(lane => {
            const rows = tasks.filter(t => lane.states.includes(t.state))
            return (
              <div key={lane.key}>
                <div className="flex items-baseline gap-2 mb-2">
                  <h3 className="text-xs uppercase tracking-wider text-gray-500">{lane.label}</h3>
                  <span className="text-xs text-gray-600 tabular-nums">{rows.length}</span>
                </div>
                <div className="space-y-2">
                  {rows.length === 0 && (
                    <div className="rounded-lg ring-1 ring-dashed ring-gray-800 p-4 text-xs text-gray-600">empty</div>
                  )}
                  {rows.map(t => <TaskCard key={t.id} task={t} onOpen={() => setOpenID(t.id)} />)}
                </div>
              </div>
            )
          })}
        </div>
      </div>

      {openID && (
        <TaskDrawer call={call} id={openID} onClose={() => setOpenID(null)} onChanged={load} />
      )}
    </div>
  )
}
