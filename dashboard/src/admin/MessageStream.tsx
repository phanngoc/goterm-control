import { useCallback, useEffect, useRef, useState } from 'react'
import type { Message } from './types'
import { ago, clock } from './format'

type Call = (method: string, params?: any) => Promise<any>

export default function MessageStream({ call, agents, selfID }: {
  call: Call; agents: string[]; selfID: string
}) {
  const [msgs, setMsgs] = useState<Message[]>([])
  const [to, setTo] = useState('')
  const [body, setBody] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const bottom = useRef<HTMLDivElement>(null)

  const load = useCallback(async () => {
    try {
      setMsgs((await call('messages.list', { limit: 200 })) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => {
    load()
    const id = setInterval(load, 4000)
    return () => clearInterval(id)
  }, [load])

  useEffect(() => {
    if (!to && agents.length) setTo(agents.find(a => a !== selfID) ?? agents[0])
  }, [agents, selfID, to])

  const send = async () => {
    if (!to || !body.trim()) return
    try {
      await call('messages.send', { to, body })
      setBody('')
      await load()
      bottom.current?.scrollIntoView({ behavior: 'smooth' })
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  // The RPC returns newest first; read the conversation oldest to newest.
  const ordered = [...msgs].reverse()

  return (
    <div className="h-full flex flex-col">
      <div className="flex-1 overflow-y-auto p-4 space-y-3">
        {err && <div className="text-xs text-red-300">{err}</div>}
        {ordered.length === 0 && !err && (
          <div className="text-sm text-gray-500">
            Nothing yet. Agents talk here with <code className="font-mono text-gray-400">bomclaw msg --to &lt;agent&gt;</code>,
            and so can you from the box below.
          </div>
        )}

        {ordered.map(m => {
          const fromSelf = m.from_agent === selfID
          return (
            <div key={m.id} className={`flex ${fromSelf ? 'justify-end' : 'justify-start'}`}>
              <div className={`max-w-[70%] rounded-lg px-3 py-2 ring-1 ${
                fromSelf ? 'bg-sky-500/10 ring-sky-500/25' : 'bg-gray-900 ring-gray-800'
              }`}>
                <div className="flex items-center gap-2 text-[11px] text-gray-500">
                  <span className="font-mono text-gray-400">{m.from_agent}</span>
                  <span>→</span>
                  <span className="font-mono text-gray-400">{m.to_agent}</span>
                  {m.task_id && (
                    <span className="px-1.5 rounded ring-1 ring-fuchsia-500/30 bg-fuchsia-500/10 text-fuchsia-300 font-mono">
                      task
                    </span>
                  )}
                  {!m.read_at && <span className="text-amber-400">unread</span>}
                  <span className="ml-auto" title={ago(m.created_at)}>{clock(m.created_at)}</span>
                </div>
                <div className="mt-1 text-sm text-gray-100 whitespace-pre-wrap break-words">{m.body}</div>
              </div>
            </div>
          )
        })}
        <div ref={bottom} />
      </div>

      <div className="p-3 border-t border-gray-800 bg-gray-900/40 flex gap-2">
        <select
          value={to}
          onChange={e => setTo(e.target.value)}
          className="px-2 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
        >
          {agents.map(a => <option key={a} value={a}>{a}</option>)}
        </select>
        <input
          value={body}
          onChange={e => setBody(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }}
          placeholder={`Message as ${selfID}…`}
          className="flex-1 px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
        />
        <button
          onClick={send}
          disabled={!body.trim() || !to}
          className="px-4 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed"
        >
          Send
        </button>
      </div>
    </div>
  )
}
