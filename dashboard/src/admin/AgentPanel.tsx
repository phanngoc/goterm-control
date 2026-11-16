import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { usePersisted } from '../lib/persisted'
import ChatInput from '../components/ChatInput'
import { AttachmentChips, PATH_MIME, PostedFiles, attachmentBlock, splitAttachments, useAttachments } from '../components/attachments'
import MessageMarkdown from '../components/MessageMarkdown'
import ProgressCard, { isProgressLine, parseProgress } from './ProgressCard'
import { LogView } from './TaskSession'
import type { Channel, ChannelMessage, TaskLogEntry } from './types'

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
  // Who you were talking to and in which session, per project; and whether
  // the open file goes along — kept across reloads like the rest of the layout.
  const [agent, setAgent] = usePersisted(`agentPick.${channelID}`, '')
  const [sessions, setSessions] = useState<ChannelMessage[]>([])
  const [session, setSession] = usePersisted<string>(`agentSession.${channelID}`, '') // thread root; '' = a new session
  const [thread, setThread] = useState<ChannelMessage[]>([])
  const [draft, setDraft] = useState('')
  const [attach, setAttach] = usePersisted('agentAttach', true)
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
        setAgent(a => (a && ids.includes(a) ? a : ids[0] || ''))
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

  // The session's steps, as the agent ran them (threads.log): every thinking
  // block, tool call and result — what Claude Code prints in its own panel.
  // Read incrementally; fast while a turn is running.
  const [log, setLog] = useState<TaskLogEntry[]>([])
  const [logRunning, setLogRunning] = useState(false)
  const lastLogID = useRef(0)
  useEffect(() => { setLog([]); lastLogID.current = 0; setLogRunning(false) }, [session])
  const loadLog = useCallback(async () => {
    if (!session) return
    try {
      const after = lastLogID.current
      const r: { entries: TaskLogEntry[]; running?: string } = await call('threads.log', { root_id: session, after })
      const fresh = r.entries ?? []
      if (after === 0) setLog(fresh)
      else if (fresh.length) setLog(prev => [...prev, ...fresh])
      for (const e of fresh) if (e.id > lastLogID.current) lastLogID.current = e.id
      setLogRunning(!!r.running)
    } catch { /* the messages still tell the story */ }
  }, [call, session])
  useEffect(() => {
    void loadLog()
    if (!session) return
    const t = setInterval(loadLog, working || logRunning ? 1500 : 5000)
    return () => clearInterval(t)
  }, [session, working, logRunning, loadLog])
  const [showThinking, setShowThinking] = usePersisted('agentThinking', true)
  const timeline = useMemo(() => turns(thread, log), [thread, log])

  // A session's agent is the one that answered it; switching to it shows that.
  useEffect(() => {
    const replier = thread.find(m => m.author_kind === 'agent')?.author_id
    if (replier && agents.includes(replier)) setAgent(replier)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, thread.length > 1])

  // Follow the bottom as steps arrive — unless you scrolled up to read, the
  // way Claude Code's panel does. Back at the bottom, it follows again.
  const stick = useRef(true)
  useEffect(() => { stick.current = true }, [session])
  useEffect(() => {
    const el = listRef.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [thread.length, last?.body, log.length])

  // Refresh the context chip whenever the panel is looked at or typed in.
  const refreshCtx = () => setCtx(getContext())
  useEffect(() => { refreshCtx() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Files from this computer, dropped or pasted: uploaded at once, sent as
  // paths the agent reads — the room's composer does the same.
  const att = useAttachments()
  // Anything dragged over the panel: a project file from the tree or a tab
  // becomes @path in the draft, as Claude Code does; a file from the desktop
  // is attached.
  const [dropping, setDropping] = useState(false)
  const dragDepth = useRef(0)
  const takes = (dt: DataTransfer) => { const t = Array.from(dt.types); return t.includes(PATH_MIME) || t.includes('Files') }
  const mention = (path: string) => setDraft(d => (d && !/\s$/.test(d) ? d + ' ' : d) + '@' + path + ' ')
  const onDrop = (e: React.DragEvent) => {
    dragDepth.current = 0; setDropping(false)
    if (e.defaultPrevented) return // the input box took the files itself
    const path = e.dataTransfer.getData(PATH_MIME)
    if (path) { e.preventDefault(); mention(path); return }
    const files = Array.from(e.dataTransfer.files ?? [])
    if (files.length) { e.preventDefault(); att.add(files) }
  }

  const send = async () => {
    const text = draft.trim()
    if ((!text && !att.paths.length) || !agent || sending || att.uploading) return
    const c = attach ? getContext() : null
    let body = [text, attachmentBlock(att.paths)].filter(Boolean).join('\n\n')
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
      att.clear()
      stick.current = true
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
    <div
      className="relative h-full flex flex-col bg-[#1e1e1e] border-l border-black/60 min-w-0"
      onDragEnter={e => { if (!takes(e.dataTransfer)) return; e.preventDefault(); dragDepth.current++; setDropping(true) }}
      onDragOver={e => { if (!takes(e.dataTransfer)) return; e.preventDefault(); e.dataTransfer.dropEffect = 'copy' }}
      onDragLeave={() => { dragDepth.current = Math.max(0, dragDepth.current - 1); if (!dragDepth.current) setDropping(false) }}
      onDrop={onDrop}
    >
      {dropping && (
        <div className="pointer-events-none absolute inset-1 z-20 grid place-items-center rounded-xl border-2 border-dashed border-sky-400/70 bg-sky-500/10 text-sm text-sky-200">
          Thả vào đây — file dự án thành @đường-dẫn, file từ máy được đính kèm
        </div>
      )}
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
        <button onClick={() => setShowThinking(v => !v)} aria-pressed={showThinking}
          title={showThinking ? 'Ẩn các đoạn suy nghĩ' : 'Hiện các đoạn suy nghĩ'}
          className={showThinking ? 'text-violet-300' : 'text-gray-500 hover:text-white'}>✻</button>
        <a
          href={`/admin/messages/${encodeURIComponent(channelID)}`} target="_blank" rel="noopener noreferrer"
          title="Mở phòng chat của dự án" className="text-gray-400 hover:text-sky-300"
        >↗</a>
        <button onClick={onClose} title="Đóng" className="text-gray-400 hover:text-white">×</button>
      </div>

      {err && <div className="px-3 py-1.5 text-xs bg-red-950/70 text-red-200 shrink-0 break-words">{err}</div>}

      <div ref={listRef}
        onScroll={e => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80 }}
        className="flex-1 min-h-0 overflow-y-auto px-3 py-3 space-y-3">
        {!session ? (
          <div className="text-sm text-gray-500 space-y-2 pt-6 px-2">
            <p className="text-gray-300">Hỏi agent về dự án này.</p>
            <p>Mỗi phiên là một thread trong phòng của dự án: agent nhớ ngữ cảnh trong phiên, và cả phòng đọc được.</p>
            <p>Bật 📎 để gửi kèm file đang mở, dòng con trỏ và đoạn code đang chọn.</p>
          </div>
        ) : timeline.map((t, i) => (
          <div key={t.key} className="space-y-2">
            {t.user && (
              <div className="pl-6">
                <div className="flex items-baseline justify-end gap-2 text-[11px] text-gray-500 mb-0.5">
                  <span>{clock(t.user.created_at)}</span><span className="text-sky-300">bạn</span>
                </div>
                <div className="rounded-lg rounded-br-sm px-3 py-2 text-[13px] bg-sky-950/50 ring-1 ring-sky-500/20">
                  <MineBody body={t.user.body} />
                </div>
              </div>
            )}
            {t.steps.length > 0 ? (
              // The turn as the agent ran it, Claude Code style: ⏺ steps,
              // ⎿ results, ✻ thinking, then its words.
              <div className="space-y-1.5 pr-1">
                <LogView entries={t.steps} running={i === timeline.length - 1 && (logRunning || working)} showThinking={showThinking} />
              </div>
            ) : t.replies.map(m => {
              const card = isProgressLine(m.body) ? parseProgress(m.body) : null
              if (card) return <ProgressCard key={m.id} p={card} author={m.author_id} fallbackSince={m.created_at} />
              return (
                <div key={m.id} className="pr-2">
                  <div className="flex items-baseline gap-2 text-[11px] text-gray-500 mb-0.5">
                    <span className="text-emerald-300">{m.author_id}</span>
                    <span>{clock(m.created_at)}</span>
                  </div>
                  <div className="flex items-start gap-1.5">
                    <span className="w-3 shrink-0 text-center text-gray-300 leading-6">⏺</span>
                    <div className="min-w-0 flex-1 text-[13px] text-gray-200"><MessageMarkdown>{stripMarker(m.body)}</MessageMarkdown></div>
                  </div>
                </div>
              )
            })}
          </div>
        ))}
        {session && working && !last?.body.startsWith(PROGRESS) && (
          <div className="text-[12px] text-gray-500 animate-pulse">{agent} đang đọc…</div>
        )}
      </div>

      <div className="shrink-0 border-t border-black/60 p-2">
        <ChatInput
          value={draft} onChange={setDraft} onSend={send} onFocus={refreshCtx}
          canSend={!!agent && !sending && !att.uploading} busy={sending}
          onFiles={att.add} hasFiles={att.paths.length > 0}
          attachments={<AttachmentChips items={att.items} onRemove={att.remove} />}
          maxHeight={260}
          placeholder={session ? 'Nhắn tiếp trong phiên này…' : 'Bắt đầu phiên mới…'}
          tools={<>
            <select
              value={agent} onChange={e => setAgent(e.target.value)}
              className="h-6 max-w-[8rem] shrink-0 rounded-full bg-[#2d2d2d] px-2 text-[11px] text-gray-200 outline-none"
              title="Agent nhận tin"
            >
              {agents.length === 0 && <option value="">(phòng chưa có agent)</option>}
              {agents.map(a => <option key={a} value={a}>to: {a}</option>)}
            </select>
            <button
              type="button"
              onClick={() => { setAttach(v => !v); refreshCtx() }}
              title="Gửi kèm file, dòng và đoạn đang chọn"
              className={`min-w-0 truncate text-[11px] ${attach ? 'text-sky-300' : 'text-gray-500 hover:text-gray-300'}`}
            >
              📎 {attach ? (ctx ? `${ctx.path}:${ctx.line}${ctx.selection ? ` · ${ctx.selection.split('\n').length} dòng` : ''}` : 'không có file mở') : 'không kèm'}
            </button>
          </>}
        />
      </div>
    </div>
  )
}

// MineBody shows what the person typed as typed, line breaks kept, and the 📎
// context the panel appended as the markdown it is (a path and a code block).
function MineBody({ body }: { body: string }) {
  const at = body.indexOf('\n\n---\n📎')
  const { text, files } = splitAttachments(at < 0 ? body : body.slice(0, at))
  return (
    <>
      <div className="whitespace-pre-wrap break-words">{text}</div>
      <PostedFiles files={files} />
      {at >= 0 && <div className="mt-1 opacity-80"><MessageMarkdown>{body.slice(at + 6)}</MessageMarkdown></div>}
    </>
  )
}

// stripMarker drops the progress marker a working line carries, for the rare
// line whose marker does not parse: its words still show, the JSON does not.
function stripMarker(body: string): string {
  const i = body.indexOf('<!--bomclaw:progress ')
  return i < 0 ? body : body.slice(0, i).trimEnd()
}

// A turn of the session: your line, then what the agent did about it. When
// the turn left a log, its steps are the answer's whole story — the reply
// message is the same words again, so it is not shown twice; a quick answer
// with no log shows as its message.
interface Turn { key: string; user?: ChannelMessage; replies: ChannelMessage[]; steps: TaskLogEntry[] }

function turns(thread: ChannelMessage[], log: TaskLogEntry[]): Turn[] {
  const out: Turn[] = []
  for (const m of thread) {
    if (m.author_kind === 'user' || out.length === 0) {
      out.push({ key: m.id, user: m.author_kind === 'user' ? m : undefined, replies: m.author_kind === 'user' ? [] : [m], steps: [] })
    } else {
      out[out.length - 1].replies.push(m)
    }
  }
  // Each step joins the last turn that began before it. Your own lines are
  // already shown from the thread, with their attachments.
  const starts = out.map(t => Date.parse(t.user?.created_at ?? t.replies[0]?.created_at ?? ''))
  for (const e of log) {
    if (e.kind === 'user') continue
    const at = Date.parse(e.created_at)
    let k = 0
    for (let i = 0; i < starts.length; i++) if (starts[i] <= at + 1000) k = i
    if (out[k]) out[k].steps.push(e)
  }
  return out
}
