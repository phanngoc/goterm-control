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

// childCounts summarises a parent's children for its card: how many, how many
// finished. Built from the flat task list, so the board needs no extra call.
function childCounts(tasks: Task[], id: string): { total: number; done: number } | null {
  const kids = tasks.filter(t => t.parent_id === id)
  if (kids.length === 0) return null
  const done = kids.filter(t => ['completed', 'failed', 'canceled', 'rejected'].includes(t.state)).length
  return { total: kids.length, done }
}

function TaskCard({ task, kids, onOpen }: { task: Task; kids: { total: number; done: number } | null; onOpen: () => void }) {
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
      <div className="mt-2 text-sm text-gray-100 leading-snug">
        {task.parent_id && <span className="text-gray-500 mr-1" title={`sub-task of ${task.parent_id}`}>↳</span>}
        {truncate(task.title, 90)}
      </div>
      {kids && (
        <div className="mt-1 text-[11px] text-gray-500" title="child tasks split off this one">
          {kids.done}/{kids.total} children finished
          {task.state === 'blocked' && task.blocked_on === 'children' && kids.done < kids.total && ' — wakes when they all have'}
        </div>
      )}
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
        <div className="mt-1.5 text-[11px] text-violet-300">
          {task.blocked_on === 'human'
            ? /* Says what to do, not just what is true — this is the one state
                 on the board that needs a person and can be cleared by one. */
              'waiting on you — click to answer'
            : `waiting on ${task.blocked_on || 'a person'}`}
        </div>
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

function TaskDrawer({ call, id, agents, onClose, onChanged, onOpenTask }: {
  call: Call; id: string; agents: string[]; onClose: () => void; onChanged: () => void; onOpenTask: (id: string) => void
}) {
  const [detail, setDetail] = useState<TaskDetail | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [answer, setAnswer] = useState('')
  const [childTitle, setChildTitle] = useState('')
  const [childBody, setChildBody] = useState('')
  const [childTo, setChildTo] = useState('')

  const load = useCallback(() => {
    call('tasks.get', { id })
      .then((d: TaskDetail) => setDetail(d))
      .catch((e: any) => setErr(String(e?.message ?? e)))
  }, [call, id])

  useEffect(() => { load() }, [load])

  // Cancelling a parent cancels its unfinished children too — say so before
  // doing it, since those may be running on another agent right now.
  const openChildren = detail?.children?.filter(c => !['completed', 'failed', 'canceled', 'rejected'].includes(c.state)).length ?? 0
  const cancel = async () => {
    if (openChildren > 0 && !confirm(`Cancel this task and its ${openChildren} unfinished child task${openChildren > 1 ? 's' : ''}?`)) return
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

  // A person splitting a piece off a task in flight: same path as the agent's
  // `bomclaw task sub`, so the same rules apply — the cap, the depth, and a
  // brief of its own, because whoever claims it has none of this conversation.
  const addChild = async () => {
    if (!childTitle.trim()) return
    setBusy(true)
    try {
      await call('tasks.create', { title: childTitle.trim(), body: childBody.trim(), parent_id: id, assigned_to: childTo || undefined })
      setChildTitle(''); setChildBody('')
      setErr(null)
      load()
      onChanged()
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

  // The answer is an instruction, not a comment: the gateway appends it to the
  // task's checkpoint, which is what the agent reads when it picks the work
  // back up. So an empty box is a no-op worth preventing.
  const sendAnswer = async () => {
    if (!answer.trim()) return
    setBusy(true)
    try {
      await call('tasks.unblock', { id, note: answer.trim() })
      setAnswer('')
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
  const awaitingPerson = t?.state === 'blocked' && t.blocked_on === 'human'
  // Blocked on its children is the system's business, not the operator's —
  // offering an answer box there would invite unblocking work that is not done.
  const blockedOnChildren = t?.state === 'blocked' && t.blocked_on === 'children'

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
                    {blockedOnChildren && <> — it resumes when its child tasks finish</>}
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

              {t.parent_id && (
                <div className="text-xs text-gray-500">
                  ↳ sub-task of{' '}
                  <button onClick={() => onOpenTask(t.parent_id!)} className="font-mono text-gray-300 hover:underline">{t.parent_id}</button>
                </div>
              )}

              {detail!.children?.length > 0 && (
                <div>
                  <div className="text-[11px] uppercase tracking-wider text-gray-600 mb-2">
                    Children · {detail!.children.filter(c => ['completed', 'failed', 'canceled', 'rejected'].includes(c.state)).length}/{detail!.children.length} finished
                  </div>
                  <ol className="space-y-1.5">
                    {detail!.children.map(c => (
                      <li key={c.id} className="flex items-baseline gap-2 text-xs">
                        <span className="text-gray-600 shrink-0">↳</span>
                        <span className={`px-1.5 rounded ring-1 shrink-0 ${taskStateStyle(c.state)}`}>{c.state}</span>
                        <button onClick={() => onOpenTask(c.id)} className="text-left text-gray-200 hover:underline truncate">{c.title}</button>
                        <span className="ml-auto font-mono text-gray-500 shrink-0">{c.claimed_by || c.assigned_to || 'any'}</span>
                      </li>
                    ))}
                  </ol>
                  {t.state === 'blocked' && t.blocked_on === 'children' && (
                    <div className="mt-1.5 text-[11px] text-violet-300">it resumes, with their results, when the last one finishes</div>
                  )}
                </div>
              )}

              {open && (
                <div className="rounded ring-1 ring-gray-800 bg-gray-900/40 p-2 space-y-1.5">
                  <div className="flex gap-2 items-center">
                    <span className="text-gray-600 text-xs shrink-0">↳ add child</span>
                    <input
                      value={childTitle}
                      onChange={e => setChildTitle(e.target.value)}
                      placeholder="a piece a peer could do in parallel…"
                      className="flex-1 px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
                    />
                    <select value={childTo} onChange={e => setChildTo(e.target.value)}
                      className="px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 text-gray-300 outline-none">
                      <option value="">any agent</option>
                      {agents.map(a => <option key={a} value={a}>{a}</option>)}
                    </select>
                    <button onClick={addChild} disabled={busy || !childTitle.trim()}
                      className="px-2.5 py-1 text-xs rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed">
                      Add
                    </button>
                  </div>
                  <textarea
                    value={childBody}
                    onChange={e => setChildBody(e.target.value)}
                    rows={2}
                    placeholder="its brief — what to do, where, what done looks like (whoever claims it has none of this task's conversation)"
                    className="w-full px-2 py-1 text-xs bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600 resize-none"
                  />
                </div>
              )}

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

              {awaitingPerson && (
                <div className="rounded ring-1 ring-violet-500/40 bg-violet-500/5 p-3">
                  <div className="text-[11px] uppercase tracking-wider text-violet-300/80 mb-2">
                    The agent is waiting on you
                  </div>
                  {t.checkpoint ? (
                    <pre className="text-xs text-gray-300 whitespace-pre-wrap font-sans mb-3">{t.checkpoint}</pre>
                  ) : (
                    <div className="text-xs text-gray-500 mb-3">
                      It parked without writing down what it needs. Say what to do next.
                    </div>
                  )}
                  <textarea
                    value={answer}
                    onChange={e => setAnswer(e.target.value)}
                    onKeyDown={e => {
                      // Enter sends; the box is one instruction, not a document.
                      // Shift+Enter still makes a newline.
                      if (e.key === 'Enter' && !e.shiftKey) {
                        e.preventDefault()
                        void sendAnswer()
                      }
                    }}
                    rows={3}
                    placeholder="Answer, or tell it what to do next…"
                    className="w-full bg-gray-900 ring-1 ring-gray-800 rounded px-2 py-1.5 text-sm text-gray-100 placeholder-gray-600 focus:outline-none focus:ring-violet-500/50 resize-y"
                  />
                  <div className="mt-2 flex items-center gap-2">
                    <button
                      onClick={sendAnswer}
                      disabled={busy || !answer.trim()}
                      className="px-3 py-1.5 text-sm rounded ring-1 ring-violet-500/40 bg-violet-500/10 text-violet-200 hover:bg-violet-500/20 disabled:opacity-40 disabled:hover:bg-violet-500/10"
                    >
                      Send &amp; unblock
                    </button>
                    <span className="text-[11px] text-gray-600">
                      goes into the task, and the agent reads it on its next run
                    </span>
                  </div>
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
                title={openChildren > 0 ? 'Its unfinished children are canceled with it' : undefined}
              >
                {openChildren > 0 ? `Cancel task + ${openChildren} child${openChildren > 1 ? 'ren' : ''}` : 'Cancel task'}
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
                  {rows.map(t => <TaskCard key={t.id} task={t} kids={childCounts(tasks, t.id)} onOpen={() => setOpenID(t.id)} />)}
                </div>
              </div>
            )
          })}
        </div>
      </div>

      {openID && (
        <TaskDrawer call={call} id={openID} agents={agents} onClose={() => setOpenID(null)} onChanged={load} onOpenTask={setOpenID} />
      )}
    </div>
  )
}
