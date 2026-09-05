import { useCallback, useEffect, useState } from 'react'
import type { Note } from './types'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

const KINDS = ['fact', 'decision', 'result', 'gotcha'] as const

const KIND_STYLE: Record<string, string> = {
  fact:     'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  decision: 'bg-violet-500/15 text-violet-300 ring-violet-500/30',
  result:   'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  gotcha:   'bg-amber-500/15 text-amber-300 ring-amber-500/30',
}

function kindStyle(kind: string) {
  return KIND_STYLE[kind] ?? 'bg-gray-500/15 text-gray-400 ring-gray-500/30'
}

export default function NotesPane({ call }: { call: Call }) {
  const [notes, setNotes] = useState<Note[]>([])
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState('')
  const [showReplaced, setShowReplaced] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [newKind, setNewKind] = useState<string>('fact')
  const [supersedes, setSupersedes] = useState<Note | null>(null)

  const load = useCallback(async () => {
    try {
      setNotes((await call('notes.list', {
        search: search || undefined,
        kind: kind || undefined,
        include_replaced: showReplaced || undefined,
        limit: 200,
      })) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call, search, kind, showReplaced])

  useEffect(() => {
    load()
    const id = setInterval(load, 10_000)
    return () => clearInterval(id)
  }, [load])

  const add = async () => {
    if (!title.trim()) return
    try {
      await call('notes.add', {
        title, body, kind: newKind,
        supersedes: supersedes?.id || undefined,
      })
      setTitle(''); setBody(''); setSupersedes(null)
      load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  return (
    <div className="h-full flex flex-col">
      <div className="p-4 border-b border-gray-800 bg-gray-900/40 space-y-2">
        {supersedes && (
          <div className="flex items-center gap-2 text-xs rounded px-2 py-1.5 bg-amber-500/10 ring-1 ring-amber-500/30">
            <span className="text-amber-300">Correcting:</span>
            <span className="text-gray-300 truncate">{supersedes.title}</span>
            <button onClick={() => setSupersedes(null)} className="ml-auto text-gray-500 hover:text-gray-300">✕</button>
          </div>
        )}
        <div className="flex gap-2">
          <input
            value={title}
            onChange={e => setTitle(e.target.value)}
            placeholder="Something worth remembering…"
            className="flex-1 px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
          />
          <select
            value={newKind}
            onChange={e => setNewKind(e.target.value)}
            className="px-2 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
          >
            {KINDS.map(k => <option key={k} value={k}>{k}</option>)}
          </select>
          <button
            onClick={add}
            disabled={!title.trim()}
            className="px-4 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {supersedes ? 'Correct' : 'Record'}
          </button>
        </div>
        <textarea
          value={body}
          onChange={e => setBody(e.target.value)}
          placeholder="Detail (optional)"
          rows={2}
          className="w-full px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600 resize-none"
        />
      </div>

      <div className="px-4 py-2 border-b border-gray-800 flex items-center gap-2">
        <input
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder="Search notes…"
          className="flex-1 px-2.5 py-1.5 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
        />
        <select
          value={kind}
          onChange={e => setKind(e.target.value)}
          className="px-2 py-1.5 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
        >
          <option value="">all kinds</option>
          {KINDS.map(k => <option key={k} value={k}>{k}</option>)}
        </select>
        <button
          onClick={() => setShowReplaced(v => !v)}
          className={`px-2 py-1.5 text-xs rounded ring-1 transition-colors ${
            showReplaced ? 'ring-gray-600 bg-gray-800 text-gray-200' : 'ring-gray-800 text-gray-500'
          }`}
        >
          history
        </button>
      </div>

      <div className="flex-1 overflow-y-auto p-4 space-y-2">
        {err && <div className="text-xs text-red-300">{err}</div>}
        {!err && notes.length === 0 && (
          <div className="text-sm text-gray-500">
            Nothing recorded yet. Agents write here with{' '}
            <code className="font-mono text-gray-400">bomclaw note add --title …</code>, and the
            markdown copy they read is regenerated on every write.
          </div>
        )}

        {notes.map(n => (
          <div
            key={n.id}
            className={`rounded-lg ring-1 p-3 ${
              n.superseded_by ? 'bg-gray-900/40 ring-gray-800/60 opacity-60' : 'bg-gray-900 ring-gray-800'
            }`}
          >
            <div className="flex items-start gap-2">
              <span className={`text-[10px] px-1.5 py-0.5 rounded ring-1 shrink-0 ${kindStyle(n.kind)}`}>
                {n.kind}
              </span>
              {n.scope !== 'shared' && (
                <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-500 shrink-0">
                  private
                </span>
              )}
              {n.superseded_by && (
                <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-500 shrink-0">
                  replaced
                </span>
              )}
              <span className="text-sm text-gray-100 leading-snug">{n.title}</span>
              {!n.superseded_by && (
                <button
                  onClick={() => { setSupersedes(n); setTitle(n.title); setBody(n.body ?? ''); setNewKind(n.kind) }}
                  title="Record a correction that replaces this note"
                  className="ml-auto shrink-0 text-[11px] text-gray-600 hover:text-gray-300"
                >
                  correct
                </button>
              )}
            </div>
            {n.body && (
              <p className="mt-1.5 text-xs text-gray-400 whitespace-pre-wrap break-words">{n.body}</p>
            )}
            <div className="mt-1.5 flex items-center gap-2 text-[11px] text-gray-600">
              <span className="font-mono">{n.author}</span>
              <span>{ago(n.created_at)}</span>
              {n.tags && <span className="font-mono">{n.tags}</span>}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
