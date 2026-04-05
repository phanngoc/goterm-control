import { useEffect } from 'react'
import { useStore, ChatMessage } from './stores/store'
import { useGateway } from './hooks/useGateway'
import SessionList from './components/SessionList'
import ChatView from './components/ChatView'
import StatusBar from './components/StatusBar'

export default function App() {
  const { call } = useGateway()
  const tab = useStore(s => s.tab)
  const setTab = useStore(s => s.setTab)
  const connected = useStore(s => s.connected)
  const setSessions = useStore(s => s.setSessions)
  const setStatus = useStore(s => s.setStatus)
  const activeSessionId = useStore(s => s.activeSessionId)

  const setActiveSessionId = useStore(s => s.setActiveSessionId)
  const setMessages = useStore(s => s.setMessages)

  // Load sessions + status on connect, auto-open latest session
  useEffect(() => {
    if (!connected) return

    const loadAll = async () => {
      try {
        const [sessions, status] = await Promise.all([
          call('sessions.list'),
          call('status'),
        ])
        setSessions(sessions || [])
        setStatus(status)

        // Auto-open the most recent session if we have one and nothing is active
        if (sessions?.length > 0 && !useStore.getState().activeSessionId) {
          const latest = sessions.sort((a: any, b: any) =>
            new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
          )[0]
          setActiveSessionId(latest.id)

          // Load its transcript
          const events = await call('transcript.get', { session_id: latest.id })
          if (Array.isArray(events) && events.length > 0) {
            setMessages(eventsToMessages(events))
            setTab('chat')
          }
        }
      } catch {}
    }

    loadAll()
    const interval = setInterval(() => {
      call('sessions.list').then((s: any) => setSessions(s || [])).catch(() => {})
      call('status').then(setStatus).catch(() => {})
    }, 10_000)
    return () => clearInterval(interval)
  }, [connected, call, setSessions, setStatus, setActiveSessionId, setMessages, setTab])

  // Auto-switch to chat when session selected
  useEffect(() => {
    if (activeSessionId) setTab('chat')
  }, [activeSessionId, setTab])

  return (
    <div className="h-full flex flex-col bg-gray-950 text-gray-100">
      {/* Header */}
      <header className="flex items-center justify-between px-4 py-2 bg-gray-900 border-b border-gray-800">
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-semibold tracking-tight">NanoClaw</h1>
          <span className={`w-2 h-2 rounded-full ${connected ? 'bg-green-400' : 'bg-red-400'}`} />
        </div>
        <nav className="flex gap-1">
          {(['sessions', 'chat', 'status'] as const).map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-3 py-1 text-sm rounded-md transition-colors ${
                tab === t ? 'bg-gray-700 text-white' : 'text-gray-400 hover:text-gray-200 hover:bg-gray-800'
              }`}
            >
              {t === 'sessions' ? 'Sessions' : t === 'chat' ? 'Chat' : 'Status'}
            </button>
          ))}
        </nav>
      </header>

      {/* Content */}
      <main className="flex-1 overflow-hidden">
        {tab === 'sessions' && <SessionList call={call} />}
        {tab === 'chat' && <ChatView call={call} />}
        {tab === 'status' && <StatusBar />}
      </main>
    </div>
  )
}

function eventsToMessages(events: any[]): ChatMessage[] {
  const msgs: ChatMessage[] = []
  let currentTools: string[] = []
  for (const ev of events) {
    switch (ev.type) {
      case 'user_message':
        msgs.push({ role: 'user', content: ev.content || '', timestamp: ev.ts })
        break
      case 'tool_call':
        currentTools.push(ev.tool_name || '')
        break
      case 'assistant_text':
        msgs.push({
          role: 'assistant', content: ev.content || '', timestamp: ev.ts,
          tools: currentTools.length > 0 ? [...currentTools] : undefined,
        })
        currentTools = []
        break
    }
  }
  return msgs
}
