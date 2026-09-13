import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Artifact, Channel, ChannelMessage } from './types'
import { ago, clock } from './format'

type Call = (method: string, params?: any) => Promise<any>

// The agents' shared room.
//
// This replaces a flat from→to stream in which every line had to pick one
// recipient, so a third agent could not read along and the human's own words
// were not in the history at all. Here the human is an ordinary member: the
// composer posts as "you" unless you deliberately speak as an agent.

export default function ChannelView({ call, agents, selfID, openThreadID, onOpenedThread, onOpenTask }: {
  call: Call; agents: string[]; selfID: string
  openThreadID?: string; onOpenedThread?: () => void; onOpenTask?: (taskID: string) => void
}) {
  const [channels, setChannels] = useState<Channel[]>([])
  const [active, setActive] = useState<string>('')
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
  const sending = useRef(false)
  const bottom = useRef<HTMLDivElement>(null)

  const loadChannels = useCallback(async () => {
    try {
      const list: Channel[] = (await call('channels.list')) || []
      setChannels(list)
      setErr(null)
      return list
    } catch (e: any) {
      setErr(String(e?.message ?? e))
      return []
    }
  }, [call])

  const loadMessages = useCallback(async (channelID: string) => {
    if (!channelID) return
    try {
      setMsgs((await call('channels.messages', { channel_id: channelID, limit: 200 })) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

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

  // First load picks the busiest room so the tab opens on something to read.
  useEffect(() => {
    let cancelled = false
    loadChannels().then(list => {
      if (cancelled || !list.length) return
      setActive(prev => prev || (list.find(c => c.mentions > 0) ?? list[0]).id)
    })
    return () => { cancelled = true }
  }, [loadChannels])

  useEffect(() => { loadMessages(active) }, [active, loadMessages])

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

  // Poll: an agent posting from its own shell has no way to push to this page.
  useEffect(() => {
    const id = setInterval(() => {
      loadChannels()
      loadMessages(active)
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
          <ChannelRow key={c.id} c={c} active={c.id === active} onPick={() => { setActive(c.id); setThread(null); setThreadRoot('') }} />
        ))}
        {channels.length === 0 && (
          <div className="px-3 py-2 text-xs text-gray-600">No channels yet.</div>
        )}
      </aside>

      {/* Main line */}
      <div className="flex-1 min-w-0 flex flex-col">
        <header className="px-4 py-2 border-b border-gray-800 flex items-baseline gap-3">
          <span className="text-sm text-gray-200 font-medium">{current?.name ?? '—'}</span>
          {current?.purpose && <span className="text-xs text-gray-500 truncate">{current.purpose}</span>}
          <span className="ml-auto text-[11px] text-gray-600 font-mono">
            {current?.members?.map(m => m.id).join(' · ')}
          </span>
        </header>

        <div className="flex-1 overflow-y-auto p-4 space-y-3">
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

function ChannelRow({ c, active, onPick }: { c: Channel; active: boolean; onPick: () => void }) {
  return (
    <button
      onClick={onPick}
      className={`w-full text-left px-3 py-1.5 text-sm flex items-center gap-2 ${
        active ? 'bg-gray-800 text-white' : 'text-gray-400 hover:bg-gray-800/50 hover:text-gray-200'
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
      <div className="mt-1 text-sm text-gray-100 whitespace-pre-wrap break-words">{m.body}</div>

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

  // Opened in a tab rather than rendered here: these are reports and patches,
  // and the thread panel is not the place to read one.
  const open = async () => {
    setBusy(true)
    try {
      if (a.kind === 'link' && a.url) {
        window.open(a.url, '_blank', 'noopener')
        return
      }
      const r = await call('artifacts.get', { id: a.id })
      const body: string = r?.content ?? ''
      const type = a.content_type || (a.title.endsWith('.html') ? 'text/html' : 'text/plain')
      const blob = new Blob([body], { type: type + ';charset=utf-8' })
      const url = URL.createObjectURL(blob)
      window.open(url, '_blank', 'noopener')
      setTimeout(() => URL.revokeObjectURL(url), 60_000)
    } catch (e) {
      alert(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex items-center gap-2 text-xs">
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
