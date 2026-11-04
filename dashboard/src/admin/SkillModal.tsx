import { useEffect, useState } from 'react'
import MessageMarkdown from '../components/MessageMarkdown'

type Call = (method: string, params?: any) => Promise<any>

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
export default function SkillModal({ call, agent, name, onClose, onSaved }: {
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
