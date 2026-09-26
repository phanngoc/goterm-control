import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Artifact, Channel, ChannelGateway, ChannelMessage, GatewayList } from './types'
import MessageMarkdown from '../components/MessageMarkdown'
import { ArtifactModal, isPage } from './ArtifactView'
import GatewayEditor from './GatewayEditor'

import { ago, clock } from './format'

type Call = (method: string, params?: any) => Promise<any>

// The agents' shared room.
//
// This replaces a flat from→to stream in which every line had to pick one
// recipient, so a third agent could not read along and the human's own words
// were not in the history at all. Here the human is an ordinary member: the
// composer posts as "you" unless you deliberately speak as an agent.

const PAGE = 30

export default function ChannelView({ call, agents, selfID, bots, channelID, onChannel, openThreadID, onOpenedThread, onOpenTask }: {
  call: Call; agents: string[]; selfID: string
  /** channelID is the room on screen, and onChannel is how it changes. The
   *  selection lives in the address bar rather than in this component, so a
   *  room can be linked, bookmarked and reloaded — every room in the sidebar
   *  used to share the one address /admin/messages. */
  channelID: string; onChannel: (id: string) => void
  /** bots maps an agent id to its own Telegram bot name, so a direct room can
   *  offer the private chat that agent actually answers on. */
  bots?: Record<string, string>
  openThreadID?: string; onOpenedThread?: () => void; onOpenTask?: (taskID: string) => void
}) {
  const [channels, setChannels] = useState<Channel[]>([])
  const active = channelID
  const setActive = onChannel
  const [msgs, setMsgs] = useState<ChannelMessage[]>([])
  const [thread, setThread] = useState<ChannelMessage[] | null>(null)
  const [threadRoot, setThreadRoot] = useState<string>('')
  const [body, setBody] = useState('')
  // The thread keeps its own draft. One shared box meant typing a reply in the
  // panel on the right while the words appeared in the box on the left.
  const [threadBody, setThreadBody] = useState('')
  // Which agent you are talking to. Always exactly one: a message to the room
  // in general is a message nobody answers, and a thread with three agents in
  // it answering at once is the same question asked three times.
  const [to, setTo] = useState<string>('')
  useEffect(() => { setTo(t => t || agents[0] || '') }, [agents])
  const [err, setErr] = useState<string | null>(null)
  // What this conversation has produced. A path named in prose is findable for
  // about a day; these are findable by id and survive the file moving.
  const [files, setFiles] = useState<Artifact[]>([])
  const [more, setMore] = useState(false)
  const [briefFor, setBriefFor] = useState('')
    // Where each room speaks outside the dashboard. Loaded for every room at
  // once — it is a short list — so the header can show a count without a call
  // per room.
  const [gateways, setGateways] = useState<ChannelGateway[]>([])
  const [gatewaysFor, setGatewaysFor] = useState('')
  const sending = useRef(false)
  const loadingOlder = useRef(false)
  const scroller = useRef<HTMLDivElement>(null)
  const atBottom = useRef(true)
  const bottom = useRef<HTMLDivElement>(null)

  const loadChannels = useCallback(async () => {
    try {
      const list: Channel[] = (await call('channels.list')) || []
      setChannels(list)
      const gw: GatewayList = await call('channels.gateways')
      setGateways(gw?.gateways ?? [])
      setErr(null)
      return list
    } catch (e: any) {
      setErr(String(e?.message ?? e))
      return []
    }
  }, [call])

  // A page, not the whole room. A reader arrives wanting the end of the
  // conversation; loading thousands of messages to show the last screenful is
  // work nobody asked for and a wait nobody wanted.
  const loadMessages = useCallback(async (channelID: string) => {
    if (!channelID) return
    try {
      const page: ChannelMessage[] = (await call('channels.messages', { channel_id: channelID, limit: PAGE })) || []
      setMsgs(page)
      setMore(page.length === PAGE)
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  // Scrolling up asks for what came before the oldest message on screen. The
  // scroll position is pinned across the insert, because a list that jumps
  // when it grows upwards is a list you cannot read.
  const loadOlder = useCallback(async () => {
    const box = scroller.current
    if (!box || !active || loadingOlder.current || !more) return
    const oldest = msgs[msgs.length - 1]
    if (!oldest) return
    loadingOlder.current = true
    const before = box.scrollHeight - box.scrollTop
    try {
      const page: ChannelMessage[] = (await call('channels.messages', {
        channel_id: active, limit: PAGE, before: oldest.created_at,
      })) || []
      if (page.length) {
        setMsgs(m => [...m, ...page])
      }
      setMore(page.length === PAGE)
      requestAnimationFrame(() => {
        if (scroller.current) scroller.current.scrollTop = scroller.current.scrollHeight - before
      })
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      loadingOlder.current = false
    }
  }, [active, call, more, msgs])

  const loadThread = useCallback(async (rootID: string) => {
    try {
      const msgs: ChannelMessage[] = (await call('channels.messages', { thread_root: rootID })) || []
      setThread(msgs)
      setThreadRoot(rootID)
      // Only a thread bound to a task can have produced anything; that binding
      // is what #134 put on the root message.
      const taskID = msgs[0]?.task_id
      setFiles(taskID ? ((await call('artifacts.list', { task_id: taskID, tree: true })) || []) : [])
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  // First load picks the busiest room so the tab opens on something to read —
  // but only when the address named no room. A link to one has to survive the
  // first load, which is the whole point of putting it in the address.
  //
  // Read through a ref, not the captured prop: the check happens after an
  // await, and whether the address had been applied by then depends on mount
  // order in another file. The ref is right whenever it is read.
  const wanted = useRef(channelID)
  wanted.current = channelID
  useEffect(() => {
    let cancelled = false
    loadChannels().then(list => {
      if (cancelled || !list.length || wanted.current) return
      setActive((list.find(c => c.mentions > 0) ?? list[0]).id)
    })
    return () => { cancelled = true }
  }, [loadChannels, setActive])

  useEffect(() => { loadMessages(active) }, [active, loadMessages])

  // Land on the newest message when a room opens, and follow it while you are
  // already at the bottom — but never yank the view down while somebody is
  // reading further up.
  useEffect(() => {
    if (!msgs.length) return
    if (atBottom.current) bottom.current?.scrollIntoView({ block: 'end' })
  }, [msgs])

  useEffect(() => { atBottom.current = true }, [active])

  // Arriving from the board: open the conversation the task came out of.
  useEffect(() => {
    if (!openThreadID) return
    call('channels.messages', { thread_root: openThreadID })
      .then((t: ChannelMessage[]) => {
        if (!t?.length) return
        setActive(t[0].channel_id)
        setThread(t)
        setThreadRoot(openThreadID)
      })
      .catch((e: any) => setErr(String(e?.message ?? e)))
      .finally(() => onOpenedThread?.())
  }, [openThreadID, call, onOpenedThread])

  // Arriving from the board: open the project's folder, which is where a
  // task's work actually lands. Selecting the room too, so closing the browser
  // leaves you somewhere that makes sense rather than on whatever was open.

  // Poll: an agent posting from its own shell has no way to push to this page.
  //
  // Only while you are at the bottom. The poll replaces the list with the
  // newest page, so running it after somebody scrolled up would throw away the
  // history they just asked for and drop them back at the end — twice a
  // minute, while they were reading.
  useEffect(() => {
    const id = setInterval(() => {
      loadChannels()
      if (atBottom.current) loadMessages(active)
      if (threadRoot) loadThread(threadRoot)
    }, 4000)
    return () => clearInterval(id)
  }, [active, threadRoot, loadChannels, loadMessages, loadThread])

  // Opening a room is reading it — but only for the human. An agent's unread
  // is its own business and must not be cleared by someone looking at a page.
  useEffect(() => {
    if (!active) return
    call('channels.read', { channel_id: active }).then(loadChannels).catch(() => {})
  }, [active, call, loadChannels])

  // One post per press. The body is only cleared once the round trip is over,
  // so without this latch a second Enter arriving before the reply re-sends the
  // same text — which is how the room ended up with a line in it twice.
  // One sender for two boxes: the channel's main line, and a thread. Which one
  // is decided by the caller, not by a mode the composer is left sitting in.
  const post = async (inThread: boolean) => {
    const text = (inThread ? threadBody : body).trim()
    if (!text || !active || !to || sending.current) return
    if (inThread && !threadRoot) return
    sending.current = true
    try {
      const posted: ChannelMessage = await call('channels.post', {
        channel_id: active, body: text, notify: [to],
        ...(inThread ? { thread_root: threadRoot } : {}),
      })
      if (inThread) setThreadBody(''); else setBody('')
      await loadMessages(active)
      if (inThread) {
        await loadThread(threadRoot)
      } else if (posted?.mentions?.length) {
        // Naming an agent is asking it something, and its answer goes into this
        // message's thread. Open that thread now so the reply arrives in view
        // instead of behind a click nobody knew to make.
        await loadThread(posted.id)
      } else {
        atBottom.current = true
        bottom.current?.scrollIntoView({ behavior: 'smooth' })
      }
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      sending.current = false
    }
  }

  // Asking for a review FILLS THE BOX; it does not send. A button that posts
  // words in your name the moment it is touched is a button you cannot try,
  // and the first thing it did was put a sentence nobody wrote into a thread.
  // Edit it, or delete it, then send — the way you would any other message.
  const reviewFile = (a: Artifact) => {
    setThreadBody(`Xem lại giúp mình artifact \`${a.id}\` (${a.title}) — đọc nội dung rồi nói thẳng chỗ nào sai, thiếu, hoặc đáng ngờ.`)
  }

  const ordered = useMemo(() => [...msgs].reverse(), [msgs])
  const current = channels.find(c => c.id === active)
  const roomGateways = gateways.filter(g => g.channel_id === active)

  return (
    <div className="h-full flex min-h-0">
      {/* Rooms */}
      <aside className="w-56 shrink-0 border-r border-gray-800 bg-gray-900/30 overflow-y-auto">
        <div className="px-3 py-2 text-[11px] uppercase tracking-wide text-gray-500">Channels</div>
        {channels.filter(c => c.kind === 'channel').map(c => (
          <ChannelRow key={c.id} c={c} active={c.id === active} onPick={() => { setActive(c.id); setThread(null); setThreadRoot('') }} />
        ))}
        <div className="px-3 py-2 mt-2 text-[11px] uppercase tracking-wide text-gray-500">Direct</div>
        {channels.filter(c => c.kind === 'dm').map(c => (
          <ChannelRow
            key={c.id} c={c} active={c.id === active} bot={bots?.[c.name]}
            onPick={() => { setActive(c.id); setThread(null); setThreadRoot('') }}
          />
        ))}
        {channels.length === 0 && (
          <div className="px-3 py-2 text-xs text-gray-600">No channels yet.</div>
        )}
        <NewProject call={call} onCreated={loadChannels} />
      </aside>

      {/* Main line */}
      <div className="flex-1 min-w-0 flex flex-col">
        <header className="px-4 py-2 border-b border-gray-800 flex items-baseline gap-3">
          <span className="text-sm text-gray-200 font-medium">{current?.name ?? '—'}</span>
          {current?.purpose && <span className="text-xs text-gray-500 truncate">{current.purpose}</span>}
          {current?.workspace && (
            // The running project — its dev server, from bomclaw.json — beside
            // its code, on the editor's own page.
            <a
              href={`/files/${encodeURIComponent(current.id)}?preview=1`}
              target="_blank"
              rel="noopener noreferrer"
              title="Chạy dự án (bomclaw.json) và xem bên cạnh code"
              className="text-[11px] px-1.5 rounded ring-1 ring-gray-700 text-gray-400 hover:text-emerald-300 hover:ring-emerald-500/40"
            >
              Preview ▸
            </a>
          )}
          {current?.workspace && (
            <button
              onClick={() => setBriefFor(current.id)}
              title={current.workspace}
              className="text-[11px] px-1.5 rounded ring-1 ring-gray-700 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40"
            >
              AGENTS.md
            </button>
          )}
          {current && (
            <button
              onClick={() => setGatewaysFor(current.id)}
              title="Nơi phòng này nói ra ngoài dashboard"
              className="text-[11px] px-1.5 rounded ring-1 ring-gray-700 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40"
            >
              Gateways
              {roomGateways.length > 0 && (
                // Amber when one is paused: a destination that has quietly
                // stopped carrying is worth seeing without opening the panel.
                <span className={`ml-1 ${roomGateways.some(g => g.mode === 'off') ? 'text-amber-300' : 'text-sky-300'}`}>
                  {roomGateways.length}
                </span>
              )}
            </button>
          )}
          <span className="ml-auto text-[11px] text-gray-600 font-mono">
            {current?.members?.map(m => m.id).join(' · ')}
          </span>
        </header>

        <div
          ref={scroller}
          onScroll={e => {
            const el = e.currentTarget
            atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
            if (el.scrollTop < 120) loadOlder()
          }}
          className="flex-1 overflow-y-auto p-4 space-y-3"
        >
          {more && (
            <div className="text-center text-[11px] text-gray-600 py-1">
              kéo lên để xem thêm…
            </div>
          )}
          {err && <div className="text-xs text-red-300">{err}</div>}
          {ordered.length === 0 && !err && (
            <div className="text-sm text-gray-500">
              Nothing here yet. Agents post with{' '}
              <code className="font-mono text-gray-400">bomclaw ch post {active || '&lt;channel&gt;'} "…"</code>.
              Write <code className="font-mono text-gray-400">@agent</code> to wake one; without a mention nobody is interrupted.
            </div>
          )}
          {ordered.map(m => (
            <Line key={m.id} m={m} selfID={selfID} onThread={() => loadThread(m.id)} onOpenTask={onOpenTask} />
          ))}
          <div ref={bottom} />
        </div>

        <Composer
          value={body} onChange={setBody} onSend={() => post(false)}
          to={to} setTo={setTo} agents={agents}
          placeholder="Message the channel"
        />
      </div>

      {briefFor && <BriefEditor call={call} channelID={briefFor} onClose={() => setBriefFor('')} />}
      {gatewaysFor && (
        <GatewayEditor
          call={call} channelID={gatewaysFor}
          channelName={channels.find(c => c.id === gatewaysFor)?.name ?? gatewaysFor}
          agents={agents} bots={bots}
          onChanged={loadChannels}
          onClose={() => setGatewaysFor('')}
        />
      )}

      {/* Thread */}
      {thread && (
        <aside className="w-96 shrink-0 border-l border-gray-800 bg-gray-900/30 flex flex-col min-h-0">
          <header className="px-3 py-2 border-b border-gray-800 flex items-center">
            <span className="text-xs uppercase tracking-wide text-gray-500">Thread</span>
            <button
              onClick={() => { setThread(null); setThreadRoot('') }}
              className="ml-auto text-xs text-gray-500 hover:text-gray-300"
            >close</button>
          </header>
          <div className="flex-1 overflow-y-auto p-3 space-y-3">
            {thread.map((m, i) => (
              <div key={m.id} className={i === 0 ? '' : 'pl-3 border-l border-gray-800'}>
                <Line m={m} selfID={selfID} compact onOpenTask={onOpenTask} />
              </div>
            ))}
          </div>
          {files.length > 0 && (
            <div className="border-t border-gray-800 px-3 py-2 space-y-1">
              <div className="text-[11px] uppercase tracking-wide text-gray-500">Files</div>
              {files.map(a => (
                <FileRow
                  key={a.id} a={a} call={call}
                  onReview={() => reviewFile(a)}
                  reviewer={to}
                />
              ))}
            </div>
          )}
          {/* Typing happens where you are reading. */}
          <Composer
            value={threadBody} onChange={setThreadBody} onSend={() => post(true)}
            to={to} setTo={setTo} agents={agents}
            placeholder="Reply in thread"
          />
        </aside>
      )}
    </div>
  )
}

function ChannelRow({ c, active, onPick, bot }: {
  c: Channel; active: boolean; onPick: () => void
  /** bot is the @name of this agent's own Telegram bot, for a direct room. Each
   *  agent here answers on a different one, so "message this one on my phone"
   *  is a different chat per agent and the row has to say which. */
  bot?: string
}) {
  return (
    <div className={`group relative flex items-center ${
      active ? 'bg-gray-800' : 'hover:bg-gray-800/50'
    }`}>
    <button
      onClick={onPick}
      className={`min-w-0 flex-1 text-left px-3 py-1.5 text-sm flex items-center gap-2 ${
        active ? 'text-white' : 'text-gray-400 group-hover:text-gray-200'
      }`}
    >
      <span className="truncate">{c.kind === 'dm' ? c.name : `# ${c.name}`}</span>
      {/* A mention is an obligation; unread is only news. Two badges, on purpose. */}
      {c.mentions > 0 && (
        <span className="ml-auto text-[10px] px-1.5 rounded-full bg-amber-500/25 text-amber-200">@{c.mentions}</span>
      )}
      {c.mentions === 0 && c.unread > 0 && (
        <span className="ml-auto text-[10px] px-1.5 rounded-full bg-gray-700 text-gray-300">{c.unread}</span>
      )}
    </button>
    {bot && (
      <a
        href={`https://t.me/${bot}`}
        target="_blank"
        rel="noopener noreferrer"
        onClick={e => e.stopPropagation()}
        title={`Nhắn riêng agent này trên Telegram — @${bot}`}
        className="shrink-0 px-2 py-1.5 text-[11px] text-gray-600 hover:text-sky-300"
      >
        ↗
      </a>
    )}
    </div>
  )
}

function Line({ m, selfID, compact, onThread, onOpenTask }: {
  m: ChannelMessage; selfID: string; compact?: boolean
  onThread?: () => void; onOpenTask?: (taskID: string) => void
}) {
  const mine = m.author_kind === 'user'
  return (
    <div className={`rounded-lg px-3 py-2 ring-1 ${
      mine ? 'bg-sky-500/10 ring-sky-500/25' : 'bg-gray-900 ring-gray-800'
    }`}>
      <div className="flex items-center gap-2 text-[11px] text-gray-500">
        <span className={`font-mono ${m.author_id === selfID ? 'text-gray-300' : 'text-gray-400'}`}>
          {m.author_kind === 'user' ? 'you' : m.author_id}
        </span>
        {m.task_id && (
          <button
            onClick={() => onOpenTask?.(m.task_id!)}
            disabled={!onOpenTask}
            title="Open this task on the board"
            className="px-1.5 rounded ring-1 ring-fuchsia-500/30 bg-fuchsia-500/10 text-fuchsia-300 font-mono enabled:hover:bg-fuchsia-500/20"
          >
            {m.task_id.slice(0, 10)}
          </button>
        )}
        {m.mentions?.map(w => (
          <span key={w} className="px-1.5 rounded bg-amber-500/15 text-amber-300 font-mono">@{w}</span>
        ))}
        <span className="ml-auto" title={ago(m.created_at)}>{clock(m.created_at)}</span>
      </div>
      {/* The agents write markdown — tables, code, headings — and a thread that
          shows it raw is a thread where a comparison table is a wall of pipes. */}
      <div className="mt-1 text-sm text-gray-100 break-words">
        <MessageMarkdown>{m.body}</MessageMarkdown>
      </div>

      {/* An answered message shows its answer. A count on its own reads like
          silence next to a question you asked an agent — which is exactly how
          the first working reply was missed. */}
      {!compact && !!m.replies && (
        <button
          onClick={onThread}
          className="mt-2 w-full text-left rounded-md pl-2 py-1 border-l-2 border-sky-500/40 bg-sky-500/5 hover:bg-sky-500/10 group"
        >
          <div className="flex items-baseline gap-2 text-[11px]">
            <span className="font-mono text-sky-300">{m.last_reply_by}</span>
            <span className="text-gray-600">{ago(m.last_reply_at)}</span>
            <span className="ml-auto text-gray-500 group-hover:text-sky-300">
              {m.replies} repl{m.replies === 1 ? 'y' : 'ies'} →
            </span>
          </div>
          <div className="text-xs text-gray-300 line-clamp-2 break-words">{m.last_reply_text}</div>
        </button>
      )}
      {!compact && !m.replies && (
        <button
          onClick={onThread}
          className="mt-1 text-[11px] text-gray-500 hover:text-sky-300"
        >
          reply in thread
        </button>
      )}
    </div>
  )
}

function Composer({ value, onChange, onSend, to, setTo, agents, placeholder }: {
  value: string; onChange: (s: string) => void; onSend: () => void
  to: string; setTo: (s: string) => void; agents: string[]; placeholder: string
}) {
  return (
    <div className="border-t border-gray-800 bg-gray-900/40">
      <div className="p-3 flex gap-2">
        {/* Who you are talking to. The author is always you — a person at a
            browser is a person — so the only choice here is which agent the
            line is addressed at. Picking one rings its doorbell without making
            anyone type the name a second time; picking nobody leaves the room
            readable and no one interrupted. */}
        <select
          value={to}
          onChange={e => setTo(e.target.value)}
          title="Which agent this is for"
          className="px-2 py-2 text-sm bg-gray-950 rounded ring-1 ring-sky-500/50 text-sky-300 outline-none"
        >
          {agents.map(a => <option key={a} value={a}>to: {a}</option>)}
        </select>
        <input
          value={value}
          onChange={e => onChange(e.target.value)}
          // Enter while an IME is composing commits the candidate — Vietnamese,
          // Japanese, Korean — and is not a send. Chrome reports that press as
          // keyCode 229; isComposing covers the rest.
          onKeyDown={e => {
            if (e.nativeEvent.isComposing || e.keyCode === 229) return
            if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); onSend() }
          }}
          placeholder={`${placeholder} — ${to || 'no agent'} will answer`}
          className="flex-1 px-3 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 focus:ring-gray-600 outline-none text-gray-200 placeholder:text-gray-600"
        />
        <button
          onClick={onSend}
          disabled={!value.trim() || !to}
          className="px-4 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed"
        >
          Send
        </button>
      </div>
    </div>
  )
}

// FileRow is one piece of work product: open it, or ask an agent about it.
function FileRow({ a, call, onReview, reviewer }: {
  a: Artifact; call: Call; onReview: () => void; reviewer: string
}) {
  const [busy, setBusy] = useState(false)

  const [content, setContent] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)

  // Markdown and text open in a modal, wide, rendered — these are reports, and
  // reading one in a 24rem side panel is not reading it. HTML and links go to
  // a tab: a page wants a browser, not a box inside one.
  const open = async () => {
    setBusy(true)
    try {
      if (a.kind === 'link' && a.url) {
        window.open(a.url, '_blank', 'noopener')
        return
      }
      const r = await call('artifacts.get', { id: a.id })
      const body: string = r?.content ?? ''
      if (isPage(a)) {
        const blob = new Blob([body], { type: 'text/html;charset=utf-8' })
        const url = URL.createObjectURL(blob)
        window.open(url, '_blank', 'noopener')
        setTimeout(() => URL.revokeObjectURL(url), 60_000)
        return
      }
      setContent(body)
      setTruncated(Boolean(r?.truncated))
    } catch (e) {
      alert(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex items-center gap-2 text-xs">
      {content !== null && (
        <ArtifactModal a={a} content={content} truncated={truncated} onClose={() => setContent(null)} />
      )}
      <button onClick={open} disabled={busy} className="min-w-0 flex-1 text-left truncate text-sky-300 hover:underline">
        {a.title}
      </button>
      <span className="text-gray-600 shrink-0">{a.kind}</span>
      <button
        onClick={onReview}
        disabled={!reviewer}
        title={reviewer ? `Soạn sẵn câu nhờ ${reviewer} xem lại — bạn bấm Send` : 'Chọn một agent trước'}
        className="shrink-0 px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40 disabled:opacity-40"
      >
        review
      </button>
    </div>
  )
}

// NewProject makes a room with a folder behind it. A project rather than a bare
// room by default, because that is what someone is making when they create a
// place for a piece of work: somewhere for the files to land and a brief
// saying what the work is.
function NewProject({ call, onCreated }: { call: Call; onCreated: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [purpose, setPurpose] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const create = async () => {
    if (!name.trim() || busy) return
    setBusy(true)
    try {
      await call('channels.create', { name: name.trim(), purpose: purpose.trim() })
      setName(''); setPurpose(''); setOpen(false); setErr(null)
      onCreated()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="w-full text-left px-3 py-1.5 mt-2 text-xs text-gray-500 hover:text-sky-300"
      >
        + dự án mới
      </button>
    )
  }
  return (
    <div className="px-3 py-2 space-y-1.5">
      <input
        autoFocus value={name} onChange={e => setName(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter' && !e.nativeEvent.isComposing) create(); if (e.key === 'Escape') setOpen(false) }}
        placeholder="tên dự án"
        className="w-full px-2 py-1 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none"
      />
      <input
        value={purpose} onChange={e => setPurpose(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter' && !e.nativeEvent.isComposing) create(); if (e.key === 'Escape') setOpen(false) }}
        placeholder="dự án này để làm gì"
        className="w-full px-2 py-1 text-xs bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
      />
      {err && <div className="text-[11px] text-red-300">{err}</div>}
      <div className="flex gap-2">
        <button
          onClick={create} disabled={!name.trim() || busy}
          className="px-2 py-1 text-xs rounded bg-gray-100 text-gray-900 disabled:opacity-40"
        >{busy ? 'đang tạo…' : 'tạo'}</button>
        <button onClick={() => setOpen(false)} className="px-2 py-1 text-xs text-gray-500 hover:text-gray-300">huỷ</button>
      </div>
      <p className="text-[11px] text-gray-600">
        Tạo kèm một thư mục và file {'AGENTS.md'} — agent đọc nó trước khi làm.
      </p>
    </div>
  )
}

// BriefEditor edits the file the agents read before working on a project.
//
// A textarea over the raw markdown rather than a form of fields: what a project
// needs said differs per project, and a form would decide that in advance. The
// file is the source of truth — agents edit it with their own tools too — so
// this saves the whole document and whoever wrote last wins, the way a shared
// file in a repository always has.
function BriefEditor({ call, channelID, onClose }: { call: Call; channelID: string; onClose: () => void }) {
  const [body, setBody] = useState<string | null>(null)
  const [path, setPath] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    call('channels.brief', { channel_id: channelID })
      .then((r: any) => { setBody(r?.body ?? ''); setPath(r?.path ?? '') })
      .catch((e: any) => setErr(String(e?.message ?? e)))
  }, [call, channelID])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const save = async () => {
    if (body === null || busy) return
    setBusy(true)
    try {
      await call('channels.brief', { channel_id: channelID, body })
      setErr(null); setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div onClick={onClose} className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-6">
      <div
        onClick={e => e.stopPropagation()}
        className="w-full max-w-3xl max-h-[88vh] flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl"
      >
        <header className="flex items-baseline gap-3 px-5 py-3 border-b border-gray-800">
          <span className="text-sm text-gray-200 font-medium">AGENTS.md</span>
          <span className="text-[11px] text-gray-600 font-mono truncate">{path}</span>
          <button onClick={onClose} className="ml-auto text-xs text-gray-500 hover:text-gray-300">close</button>
        </header>
        <div className="flex-1 min-h-0 p-4">
          {body === null ? (
            <div className="text-sm text-gray-500">Loading…</div>
          ) : (
            <textarea
              value={body}
              onChange={e => setBody(e.target.value)}
              spellCheck={false}
              className="w-full h-[60vh] px-3 py-2 text-sm font-mono bg-gray-900 rounded ring-1 ring-gray-800 text-gray-200 outline-none focus:ring-gray-600 resize-none"
            />
          )}
        </div>
        <footer className="flex items-center gap-3 px-5 py-3 border-t border-gray-800">
          {err && <span className="text-xs text-red-300">{err}</span>}
          {saved && <span className="text-xs text-emerald-300">đã lưu</span>}
          <span className="ml-auto text-[11px] text-gray-600">
            Agent đọc file này trước khi làm việc trong dự án.
          </span>
          <button
            onClick={save} disabled={busy || body === null}
            className="px-3 py-1.5 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40"
          >{busy ? 'đang lưu…' : 'Lưu'}</button>
        </footer>
      </div>
    </div>
  )
}

