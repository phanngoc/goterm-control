import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import MessageMarkdown from '../components/MessageMarkdown'
import type { Channel, ChannelMessage } from './types'

type Call = (method: string, params?: any) => Promise<any>

/** What the editor is looking at, offered to the agent with a message. */
export interface EditorContext {
  path: string
  line: number
  language: string
  selection: string
}

const PROGRESS = '⏳ '
const MAX_SELECTION = 4000
const errText = (e: any) => String(e?.message ?? e)
const firstLine = (s: string) => s.replace(/^@\S+\s*/, '').split('\n')[0].slice(0, 70) || '(trống)'
const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })

// AgentPanel talks to the project's agents from inside the editor.
//
// A session is a thread in the project's room. That is not a shortcut: it is
// what a session already is here — an agent answering a mention runs one CLI
// session per thread (ch_cm_<root>), so a thread keeps its context across
// messages, stays readable in the room, and any agent in the room can be
// asked. The editor adds the one thing the room cannot: what you are looking
// at — the open file, the line, the selected code.
export default function AgentPanel({ call, channelID, getContext, onClose }: {
  call: Call
  channelID: string
  getContext: () => EditorContext | null
  onClose: () => void
}) {
  const [agents, setAgents] = useState<string[]>([])
  const [agent, setAgent] = useState('')
  const [sessions, setSessions] = useState<ChannelMessage[]>([])
  const [session, setSession] = useState<string>('') // thread root; '' = a new session
  const [thread, setThread] = useState<ChannelMessage[]>([])
  const [draft, setDraft] = useState('')
  const [attach, setAttach] = useState(true)
  const [sending, setSending] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const [ctx, setCtx] = useState<EditorContext | null>(null)

  // Who can be asked: the agents in this room.
  useEffect(() => {
    call('channels.list')
      .then((cs: Channel[]) => {
        const ch = (cs || []).find(c => c.id === channelID)
        const ids = (ch?.members ?? []).filter(m => m.kind === 'agent').map(m => m.id)
        setAgents(ids)
        setAgent(a => a || ids[0] || '')
      })
      .catch(e => setErr(errText(e)))
  }, [call, channelID])

  const loadSessions = useCallback(async () => {
    try {
      const roots: ChannelMessage[] = (await call('channels.messages', { channel_id: channelID, limit: 60 })) || []
      roots.sort((a, b) => (b.last_reply_at || b.created_at).localeCompare(a.last_reply_at || a.created_at))
      setSessions(roots)
    } catch (e) {
      setErr(errText(e))
    }
  }, [call, channelID])

  useEffect(() => {
    loadSessions()
    const t = setInterval(loadSessions, 8000)
    return () => clearInterval(t)
  }, [loadSessions])

  const loadThread = useCallback(async (root: string) => {
    if (!root) { setThread([]); return }
    try {
      setThread((await call('channels.messages', { thread_root: root })) || [])
    } catch (e) {
      setErr(errText(e))
    }
  }, [call])

  // While the agent is working — its progress line is the last word, or the
  // person spoke last and nobody has answered — poll fast; otherwise slowly.
  const last = thread[thread.length - 1]
  const working = !!last && (last.body.startsWith(PROGRESS) || last.author_kind === 'user')
  useEffect(() => {
    loadThread(session)
    if (!session) return
    const t = setInterval(() => loadThread(session), working ? 1500 : 6000)
    return () => clearInterval(t)
  }, [session, working, loadThread])

  // A session's agent is the one that answered it; switching to it shows that.
  useEffect(() => {
    const replier = thread.find(m => m.author_kind === 'agent')?.author_id
    if (replier && agents.includes(replier)) setAgent(replier)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, thread.length > 1])

  useEffect(() => {
    const el = listRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [thread.length, last?.body])

  // Refresh the context chip whenever the panel is looked at or typed in.
  const refreshCtx = () => setCtx(getContext())
  useEffect(() => { refreshCtx() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const send = async () => {
    const text = draft.trim()
    if (!text || !agent || sending) return
    const c = attach ? getContext() : null
    let body = text
    if (c) {
      body += `\n\n---\n📎 \`${c.path}:${c.line}\``
      if (c.selection) {
        const sel = c.selection.length > MAX_SELECTION ? c.selection.slice(0, MAX_SELECTION) + '\n…' : c.selection
        body += `\n\`\`\`${c.language}\n${sel}\n\`\`\``
      }
    }
    setSending(true)
    setErr(null)
    try {
      const msg: ChannelMessage = await call('channels.post', {
        channel_id: channelID, body, notify: [agent],
        ...(session ? { thread_root: session } : {}),
      })
      setDraft('')
      if (!session) setSession(msg.id)
      else loadThread(session)
      loadSessions()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setSending(false)
    }
  }

  const title = useMemo(() => {
    const root = sessions.find(s => s.id === session)
    return root ? firstLine(root.body) : 'Phiên mới'
  }, [sessions, session])

  return (
    <div className="h-full flex flex-col bg-[#1e1e1e] border-l border-black/60 min-w-0">
      <div className="flex items-center gap-2 h-9 px-2 bg-[#181818] shrink-0 text-[12px]">
        <span className="text-[11px] uppercase tracking-wide text-gray-500">Agent</span>
        <select
          value={session}
          onChange={e => setSession(e.target.value)}
          title={title}
          className="flex-1 min-w-0 h-6 rounded bg-[#2d2d2d] px-1 text-gray-200 outline-none"
        >
          <option value="">＋ Phiên mới</option>
          {sessions.map(s => (
            <option key={s.id} value={s.id}>
              {firstLine(s.body)}{s.replies ? ` · ${s.replies}` : ''}
            </option>
          ))}
        </select>
        <button onClick={() => setSession('')} title="Phiên mới" className="text-gray-400 hover:text-white">＋</button>
        <a
          href={`/admin/messages/${encodeURIComponent(channelID)}`} target="_blank" rel="noopener noreferrer"
          title="Mở phòng chat của dự án" className="text-gray-400 hover:text-sky-300"
        >↗</a>
        <button onClick={onClose} title="Đóng" className="text-gray-400 hover:text-white">×</button>
      </div>

      {err && <div className="px-3 py-1.5 text-xs bg-red-950/70 text-red-200 shrink-0 break-words">{err}</div>}

      <div ref={listRef} className="flex-1 min-h-0 overflow-y-auto px-3 py-3 space-y-3">
        {!session ? (
          <div className="text-sm text-gray-500 space-y-2 pt-6 px-2">
            <p className="text-gray-300">Hỏi agent về dự án này.</p>
            <p>Mỗi phiên là một thread trong phòng của dự án: agent nhớ ngữ cảnh trong phiên, và cả phòng đọc được.</p>
            <p>Bật 📎 để gửi kèm file đang mở, dòng con trỏ và đoạn code đang chọn.</p>
          </div>
        ) : thread.map(m => {
          const mine = m.author_kind === 'user'
          const progress = m.body.startsWith(PROGRESS)
          return (
            <div key={m.id} className={mine ? 'pl-6' : 'pr-2'}>
              <div className="flex items-baseline gap-2 text-[11px] text-gray-500 mb-0.5">
                <span className={mine ? 'text-sky-300' : 'text-emerald-300'}>{mine ? 'bạn' : m.author_id}</span>
                <span>{clock(m.created_at)}</span>
              </div>
              <div className={`rounded-lg px-3 py-2 text-[13px] ${mine ? 'bg-sky-950/50' : progress ? 'bg-amber-950/30 text-gray-300' : 'bg-[#252526]'}`}>
                <MessageMarkdown>{m.body}</MessageMarkdown>
              </div>
            </div>
          )
        })}
        {session && working && !last?.body.startsWith(PROGRESS) && (
          <div className="text-[12px] text-gray-500 animate-pulse">{agent} đang đọc…</div>
        )}
      </div>

      <div className="shrink-0 border-t border-black/60 p-2 space-y-1.5">
        <div className="flex items-center gap-2 text-[11px] text-gray-500">
          <select
            value={agent} onChange={e => setAgent(e.target.value)}
            className="h-6 rounded bg-[#2d2d2d] px-1 text-gray-200 outline-none"
            title="Agent nhận tin"
          >
            {agents.length === 0 && <option value="">(phòng chưa có agent)</option>}
            {agents.map(a => <option key={a} value={a}>to: {a}</option>)}
          </select>
          <button
            onClick={() => { setAttach(v => !v); refreshCtx() }}
            title="Gửi kèm file, dòng và đoạn đang chọn"
            className={`truncate ${attach ? 'text-sky-300' : 'text-gray-500 hover:text-gray-300'}`}
          >
            📎 {attach ? (ctx ? `${ctx.path}:${ctx.line}${ctx.selection ? ` · ${ctx.selection.split('\n').length} dòng chọn` : ''}` : 'không có file mở') : 'không kèm ngữ cảnh'}
          </button>
        </div>
        <textarea
          value={draft}
          onChange={e => setDraft(e.target.value)}
          onFocus={refreshCtx}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); send() }
          }}
          rows={3}
          placeholder={session ? 'Nhắn tiếp trong phiên này… (Enter gửi, Shift+Enter xuống dòng)' : 'Bắt đầu phiên mới… (Enter gửi)'}
          className="w-full resize-none rounded bg-[#2d2d2d] px-2 py-1.5 text-[13px] text-gray-100 outline-none ring-1 ring-transparent focus:ring-sky-700 placeholder:text-gray-500"
        />
        <div className="flex justify-end">
          <button
            onClick={send} disabled={!draft.trim() || !agent || sending}
            className="px-3 py-1 rounded text-[12px] bg-sky-700 text-white hover:bg-sky-600 disabled:opacity-40"
          >{sending ? 'đang gửi…' : 'Gửi'}</button>
        </div>
      </div>
    </div>
  )
}
