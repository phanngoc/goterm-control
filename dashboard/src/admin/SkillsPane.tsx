import { useCallback, useEffect, useState } from 'react'
import type { AgentSkills, HubSkill } from './types'
import MessageMarkdown from '../components/MessageMarkdown'

type Call = (method: string, params?: any) => Promise<any>

// The skills hub.
//
// Three agents, three toolkits, changed while they run — the index is re-read
// every turn, so everything on this screen takes effect on the agent's next
// turn without a restart.
//
// Laid out by agent rather than by skill, because the question people arrive
// with is "what can this one do", not "who has deploy". The cross-agent view is
// what the copy button is for.

// SkillModal is where a skill is actually read.
//
// Reading comes first and editing is a mode, for the same reason the project
// file browser works that way: a skill is mostly a thing you open to find out
// what an agent has been told, and dropping straight into a textarea invites a
// stray keystroke into instructions the agent will follow on its next turn.
//
// Rendered rather than shown as source, because a SKILL.md is written to be
// read — headings, steps, tables — and reading one as raw markdown in a
// monospace box is not reading it.
function SkillModal({ call, agent, name, onClose, onSaved }: {
  call: Call; agent: string; name: string; onClose: () => void; onSaved: () => void
}) {
  const [body, setBody] = useState<string | null>(null)
  const [draft, setDraft] = useState<string | null>(null)
  const [original, setOriginal] = useState<string | null>(null)
  const [showOriginal, setShowOriginal] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    call('skills.get', { agent_id: agent, name })
      .then((r: any) => setBody(r?.body ?? ''))
      .catch((e: any) => setErr(String(e?.message ?? e)))
    // The shipped version, for reading beside a copy the agent has changed. A
    // failure here is the ordinary case — most skills are not bundled — so it
    // is not an error.
    call('skills.get', { name, bundled: true })
      .then((r: any) => setOriginal(r?.body ?? null))
      .catch(() => setOriginal(null))
  }, [call, agent, name])

  // Escape closes, but never out of an unsaved edit: losing a draft to a
  // keystroke is the one thing a reader does not expect.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && draft === null) onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose, draft])

  const save = async () => {
    if (draft === null) return
    setBusy(true)
    try {
      await call('skills.install', { agent_id: agent, name, body: draft })
      setBody(draft)
      setDraft(null)
      setErr(null)
      onSaved()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const shown = showOriginal ? original : body

  return (
    <div
      onClick={() => { if (draft === null) onClose() }}
      className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-6"
    >
      <div onClick={e => e.stopPropagation()}
        className="w-full max-w-4xl max-h-[88vh] flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl">
        <header className="flex items-center gap-3 px-5 py-3 border-b border-gray-800">
          <span className="text-sm text-gray-200 font-medium">{name}</span>
          <span className="text-[11px] text-gray-600 font-mono">{agent}</span>

          {original !== null && draft === null && (
            <button
              onClick={() => setShowOriginal(v => !v)}
              title="So bản của agent này với bản gốc đi kèm"
              className="text-[11px] text-sky-300 hover:underline"
            >
              {showOriginal ? 'bản của agent' : 'bản gốc đi kèm'}
            </button>
          )}

          <div className="ml-auto flex items-center gap-2">
            {draft === null && !showOriginal && body !== null && (
              <button
                onClick={() => setDraft(body)}
                className="px-2 py-1 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40"
              >
                sửa
              </button>
            )}
            <button
              onClick={onClose}
              disabled={draft !== null}
              title={draft !== null ? 'Lưu hoặc huỷ trước đã' : undefined}
              className="text-xs text-gray-500 hover:text-gray-300 disabled:opacity-40"
            >
              đóng
            </button>
          </div>
        </header>

        <div className="flex-1 overflow-y-auto px-6 py-4">
          {err && <div className="mb-3 text-xs text-red-300">{err}</div>}

          {draft !== null ? (
            <textarea
              value={draft}
              onChange={e => setDraft(e.target.value)}
              spellCheck={false}
              autoFocus
              className="w-full h-[58vh] bg-gray-900 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none
                         p-3 text-xs font-mono text-gray-200 resize-none"
            />
          ) : shown === null ? (
            <div className="text-sm text-gray-500">Đang tải…</div>
          ) : (
            <MessageMarkdown wide>{shown}</MessageMarkdown>
          )}
        </div>

        {draft !== null && (
          <div className="px-5 py-3 border-t border-gray-800 flex items-center gap-2">
            <button onClick={save} disabled={busy}
              className="px-3 py-1.5 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40">
              Lưu
            </button>
            <button onClick={() => { setDraft(null); setErr(null) }}
              className="px-2 py-1.5 text-sm text-gray-500 hover:text-gray-300">
              huỷ
            </button>
            <span className="text-[11px] text-gray-600">
              có hiệu lực ở lượt kế tiếp của agent — không cần restart
            </span>
          </div>
        )}
      </div>
    </div>
  )
}

// SkillRow — the whole row opens the skill, the controls on the right do not.
//
// The name alone was the click target and nobody found it: it renders as a
// heading, and a heading is not something people try clicking. A task card on
// the board opens the same way, so this is the pattern the screen already has.
function SkillRow({ skill, agent, peers, onOpen, onRemove, onCopy }: {
  skill: HubSkill; agent: string; peers: string[]
  onOpen: () => void; onRemove: () => void; onCopy: (to: string) => void
}) {
  return (
    <li className="border-b border-gray-800/60 last:border-0">
      <div
        onClick={onOpen}
        role="button"
        tabIndex={0}
        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen() } }}
        title="Mở để đọc toàn văn"
        className="group py-2 -mx-1 px-1 rounded cursor-pointer hover:bg-gray-800/40
                   focus:outline-none focus:ring-1 focus:ring-sky-500/40"
      >
        <div className="flex items-baseline gap-2">
          <span className="text-sm text-gray-100 group-hover:text-sky-300">{skill.name}</span>

          {skill.edited && (
            <span
              title="Bản của agent này đã khác bản gốc — nó học được điều gì đó các con khác chưa có"
              className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-amber-500/30 bg-amber-500/10 text-amber-300"
            >
              đã sửa
            </span>
          )}
          {skill.category && (
            <span
              title="Backend của agent tự xếp skill này vào nhóm đó"
              className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-500 font-mono"
            >
              {skill.category}
            </span>
          )}
          {!skill.bundled && (
            <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-500">riêng</span>
          )}

          {/* Says what a click does, and only while the row is under the
              cursor — a permanent label on every row is nine labels nobody
              reads. */}
          <span className="text-[10px] text-sky-300/0 group-hover:text-sky-300/80 transition-colors">đọc</span>

          {/* The controls are inside the clickable row, so each one has to stop
              the click reaching it. A "gỡ" that also opened the reader would be
              a confirm dialog behind a modal. */}
          <div className="ml-auto flex items-center gap-1.5" onClick={e => e.stopPropagation()}>
            {peers.length > 0 && (
              <select
                value=""
                onChange={e => { if (e.target.value) onCopy(e.target.value) }}
                title="Chép bản này sang agent khác"
                className="px-1.5 py-0.5 text-[11px] bg-gray-950 rounded ring-1 ring-gray-800 text-gray-400 outline-none"
              >
                <option value="">chép sang…</option>
                {peers.map(p => <option key={p} value={p}>{p}</option>)}
              </select>
            )}
            <button onClick={onRemove}
              className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-500 hover:text-red-300 hover:ring-red-500/40">
              gỡ
            </button>
          </div>
        </div>

        <p className="mt-0.5 text-xs text-gray-500 leading-snug">
          {skill.description || <span className="text-gray-600 italic">chưa ai nói khi nào dùng nó</span>}
        </p>
        <p className="mt-0.5 text-[10px] text-gray-700 font-mono break-all">{skill.path}</p>
        <span className="sr-only">{agent}</span>
      </div>
    </li>
  )
}

export default function SkillsPane({ call }: { call: Call }) {
  const [rows, setRows] = useState<AgentSkills[]>([])
  const [err, setErr] = useState<string | null>(null)
  // Which skill is open for reading. Editing is a mode inside that, not a
  // separate state: you read first and then decide to change something.
  const [open, setOpen] = useState<{ agent: string; name: string } | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setRows((await call('skills.list')) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => { load() }, [load])

  const act = async (method: string, params: any, confirmText?: string) => {
    if (confirmText && !confirm(confirmText)) return
    setBusy(true)
    try {
      await call(method, params)
      setErr(null)
      await load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const agentIDs = rows.map(r => r.agent_id)

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="mb-4 max-w-2xl">
        <h2 className="text-sm text-gray-200 font-medium">Kĩ năng</h2>
        <p className="mt-1 text-xs text-gray-500 leading-relaxed">
          Mỗi agent giữ <strong className="text-gray-400">bản riêng</strong> của từng skill, và tự sửa được
          bản của mình. Nên một con học được điều gì thì bản của nó khác đi — dấu{' '}
          <span className="text-amber-300">đã sửa</span> là chỗ đó. <em>Chép sang</em> là cách một cải tiến
          lan ra; không có nó thì ba agent giỏi lên một mình.
        </p>
        <p className="mt-1 text-xs text-gray-600">
          Mọi thay đổi có hiệu lực ở lượt kế tiếp của agent, không cần restart.
        </p>
      </div>

      {err && <div className="mb-3 text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2">{err}</div>}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {rows.map(r => (
          <section key={r.agent_id} className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
            <header className="flex items-baseline gap-2 pb-2 border-b border-gray-800">
              <span className="font-mono text-sm text-gray-100">{r.agent_id}</span>
              {r.provider && <span className="text-[11px] text-gray-500">{r.provider}</span>}
              <span className={`ml-auto text-[10px] px-1.5 py-0.5 rounded ring-1 ${
                r.online
                  ? 'ring-emerald-500/30 bg-emerald-500/10 text-emerald-300'
                  : 'ring-gray-700 text-gray-500'
              }`}>
                {r.online ? 'online' : 'offline'}
              </span>
            </header>

            {r.error ? (
              <p className="pt-2 text-xs text-red-300">{r.error}</p>
            ) : (
              <ul className="pt-1">
                {r.skills.length === 0 && (
                  <li className="py-3 text-xs text-gray-600">chưa có skill nào</li>
                )}
                {r.skills.map(s => (
                  <SkillRow
                    key={s.name}
                    skill={s}
                    agent={r.agent_id}
                    peers={agentIDs.filter(a => a !== r.agent_id)}
                    onOpen={() => setOpen({ agent: r.agent_id, name: s.name })}
                    onRemove={() => act('skills.remove', { agent_id: r.agent_id, name: s.name },
                      s.edited
                        ? `Gỡ "${s.name}" khỏi ${r.agent_id}?\n\nBản này đã được sửa — những gì agent tự thêm vào sẽ mất. Bản gốc đi kèm thì lấy lại được.`
                        : `Gỡ "${s.name}" khỏi ${r.agent_id}?`)}
                    onCopy={to => act('skills.copy',
                      { from: r.agent_id, agent_id: to, name: s.name },
                      `Chép bản "${s.name}" của ${r.agent_id} sang ${to}?\n\nBản hiện tại của ${to} sẽ bị thay.`)}
                  />
                ))}
              </ul>
            )}

            {(r.missing?.length ?? 0) > 0 && (
              <div className="pt-2 mt-1 border-t border-gray-800">
                <div className="text-[10px] uppercase tracking-wider text-gray-600 mb-1">chưa có</div>
                <div className="flex flex-wrap gap-1.5">
                  {r.missing!.map(n => (
                    <button
                      key={n}
                      disabled={busy}
                      onClick={() => act('skills.install', { agent_id: r.agent_id, name: n })}
                      title="Cài lại bản gốc đi kèm"
                      className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40"
                    >
                      + {n}
                    </button>
                  ))}
                </div>
              </div>
            )}
          </section>
        ))}
      </div>

      {open && (
        <SkillModal
          call={call} agent={open.agent} name={open.name}
          onClose={() => setOpen(null)} onSaved={load}
        />
      )}
    </div>
  )
}
