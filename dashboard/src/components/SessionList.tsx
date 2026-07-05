import { useStore, Session } from '../stores/store'
import { eventsToMessages } from '../lib/transcript'

function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime()
  const mins = Math.floor(diff / 60_000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}

export default function SessionList({ call }: { call: (m: string, p?: any) => Promise<any> }) {
  const rawSessions = useStore(s => s.sessions)
  // Newest first — the server already sorts, but keep the invariant client-side too.
  const sessions = [...rawSessions].sort(
    (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
  )
  const setActiveSessionId = useStore(s => s.setActiveSessionId)
  const setMessages = useStore(s => s.setMessages)
  const setTab = useStore(s => s.setTab)

  const openSession = async (session: Session) => {
    setActiveSessionId(session.id)
    setMessages([])

    // Load transcript
    try {
      const events = await call('transcript.get', { session_id: session.id })
      if (Array.isArray(events)) {
        const msgs = eventsToMessages(events)
        setMessages(msgs)
      }
    } catch (e) {
      console.error('Failed to load transcript:', e)
    }
    setTab('chat')
  }

  const resetSession = async (session: Session, e: React.MouseEvent) => {
    e.stopPropagation()
    if (!confirm(`Reset session ${session.id}?`)) return
    await call('sessions.reset', { chat_id: session.chat_id })
    await call('sessions.list').then(useStore.getState().setSessions)
  }

  if (sessions.length === 0) {
    return (
      <div className="flex items-center justify-center h-full text-gray-500">
        <div className="text-center">
          <p className="text-lg">No sessions yet</p>
          <p className="text-sm mt-1">Send a message on Telegram or start a new chat</p>
          <button
            onClick={() => {
              const id = 'chat_' + Date.now()
              setActiveSessionId(id)
              setMessages([])
              setTab('chat')
            }}
            className="mt-4 px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-500 transition-colors"
          >
            New Chat
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-medium text-gray-300">Sessions</h2>
        <button
          onClick={() => {
            const id = 'chat_' + Date.now()
            setActiveSessionId(id)
            setMessages([])
            setTab('chat')
          }}
          className="px-3 py-1.5 text-sm bg-blue-600 text-white rounded-lg hover:bg-blue-500 transition-colors"
        >
          + New Chat
        </button>
      </div>
      <div className="space-y-2">
        {sessions.map(s => (
          <div
            key={s.id}
            onClick={() => openSession(s)}
            className="flex items-center justify-between p-3 bg-gray-900 rounded-lg border border-gray-800 hover:border-gray-600 cursor-pointer transition-colors"
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="text-sm text-gray-200 truncate">{s.label || s.id}</span>
                <span className="text-xs text-gray-500 shrink-0">{s.message_count} turns</span>
              </div>
              <div className="text-xs text-gray-500 mt-0.5 truncate">
                {timeAgo(s.updated_at)} · {s.input_tokens + s.output_tokens} tokens
                {s.label ? <span className="font-mono"> · {s.id}</span> : null}
              </div>
            </div>
            <button
              onClick={(e) => resetSession(s, e)}
              className="text-xs text-gray-500 hover:text-red-400 px-2 py-1 rounded transition-colors"
            >
              Reset
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}

