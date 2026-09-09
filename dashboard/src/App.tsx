import { useEffect, useState } from 'react'
import { useStore } from './stores/store'
import { useGateway } from './hooks/useGateway'
import { eventsToMessages } from './lib/transcript'
import SessionList from './components/SessionList'
import ChatView from './components/ChatView'
import StatusBar from './components/StatusBar'
import AdminView from './admin/AdminView'
import type { Me } from './Root'
import { parseRoute, pathFor, type AdminPane } from './lib/route'

export default function App({ me, onLogout }: { me: Me; onLogout?: () => void }) {
  const { call } = useGateway()
  const tab = useStore(s => s.tab)
  const setTab = useStore(s => s.setTab)
  const connected = useStore(s => s.connected)
  const setSessions = useStore(s => s.setSessions)
  const setStatus = useStore(s => s.setStatus)
  const activeSessionId = useStore(s => s.activeSessionId)
  const setActiveSessionId = useStore(s => s.setActiveSessionId)
  const setMessages = useStore(s => s.setMessages)
  const sessions = useStore(s => s.sessions)
  const externalTurn = useStore(s => s.externalTurn)
  const sending = useStore(s => s.sending)

  // Which admin pane is on screen. Lifted out of AdminView so that one place
  // owns the address bar; AdminView is now told which pane to show.
  const [adminPane, setAdminPane] = useState<AdminPane>('overview')

  // Another channel wrote to a session — Telegram, or an agent that claimed a
  // task. Refresh the list (labels, counts), and if that session is the one on
  // screen and we are not mid-send ourselves, reload its transcript so the web
  // shows what Telegram shows, the moment it happens. While the other side is
  // still working ("started" with no "finished" yet) poll every 5s so its
  // partial snapshots appear too; "finished" cancels the poll and reloads once.
  useEffect(() => {
    if (!externalTurn) return
    call('sessions.list').then((s: any) => setSessions(s || [])).catch(() => {})
    if (externalTurn.sessionId !== activeSessionId || sending) return

    const reload = () =>
      call('transcript.get', { session_id: externalTurn.sessionId })
        .then((events: any) => { if (Array.isArray(events)) setMessages(eventsToMessages(events)) })
        .catch(() => {})
    reload()
    if (externalTurn.phase !== 'started') return
    const t = setInterval(reload, 5000)
    return () => clearInterval(t)
  }, [externalTurn, activeSessionId, sending, call, setSessions, setMessages])

  // Sync URL → state, on load and whenever the browser navigates. The popstate
  // half is new: without it Back and Forward changed the address bar and left
  // the view where it was.
  useEffect(() => {
    const apply = () => {
      const r = parseRoute(location.pathname)
      setTab(r.tab)
      if (r.sessionId) setActiveSessionId(r.sessionId)
      if (r.adminPane) setAdminPane(r.adminPane)
    }
    apply()
    window.addEventListener('popstate', apply)
    return () => window.removeEventListener('popstate', apply)
  }, [setActiveSessionId, setTab])

  // State → URL. pushState rather than replaceState, so moving between tabs
  // leaves history to go back through; and only when the path actually differs,
  // which keeps the effect from stacking an entry per render (including the one
  // right after the read above).
  //
  // The exception is filling in a detail on the tab already showing — /chat
  // becoming /chat/<id> when the newest session is auto-selected. That is the
  // same navigation, not a second one: pushing it would leave a /chat entry
  // that Back returns to and the auto-select immediately leaves again.
  useEffect(() => {
    const path = pathFor({ tab, sessionId: activeSessionId ?? undefined, adminPane })
    const here = location.pathname
    if (path === here) return
    const refines = here !== '/' && path.startsWith(here + '/')
    history[refines ? 'replaceState' : 'pushState'](null, '', path)
  }, [tab, activeSessionId, adminPane])

  // Load sessions + status on connect
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

        // If URL points to a session, load its transcript
        const urlSessionId = useStore.getState().activeSessionId
        if (urlSessionId && urlSessionId !== 'new') {
          const events = await call('transcript.get', { session_id: urlSessionId })
          if (Array.isArray(events) && events.length > 0) {
            setMessages(eventsToMessages(events))
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
  }, [connected, call, setSessions, setStatus, setMessages])

  // Auto-switch to chat when session selected
  useEffect(() => {
    if (activeSessionId) setTab('chat')
  }, [activeSessionId, setTab])

  // Opening Chat with nothing selected used to show an empty conversation even
  // when one was in progress — the history was only reachable by going through
  // Sessions first. Land on the most recent session instead; "+ New Chat" is
  // how you deliberately start a blank one.
  useEffect(() => {
    if (tab !== 'chat' || activeSessionId || sessions.length === 0) return
    const newest = sessions[0]
    setActiveSessionId(newest.id)
    call('transcript.get', { session_id: newest.id })
      .then((events: any) => {
        if (Array.isArray(events) && events.length > 0) setMessages(eventsToMessages(events))
      })
      .catch(() => {})
  }, [tab, activeSessionId, sessions, setActiveSessionId, setMessages, call])

  return (
    <div className="h-full flex flex-col bg-gray-950 text-gray-100">
      <header className="flex items-center justify-between px-4 py-2 bg-gray-900 border-b border-gray-800">
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-semibold tracking-tight cursor-pointer" onClick={() => { setTab('sessions'); setActiveSessionId(null) }}>
            BomClaw
          </h1>
          <span className={`w-2 h-2 rounded-full ${connected ? 'bg-green-400' : 'bg-red-400'}`} />
        </div>
        <nav className="flex items-center gap-1">
          {(['sessions', 'chat', 'status', 'admin'] as const).map(t => (
            <button
              key={t}
              onClick={() => { setTab(t); if (t === 'sessions') setActiveSessionId(null) }}
              className={`px-3 py-1 text-sm rounded-md transition-colors ${
                tab === t ? 'bg-gray-700 text-white' : 'text-gray-400 hover:text-gray-200 hover:bg-gray-800'
              }`}
            >
              {t === 'sessions' ? 'Sessions' : t === 'chat' ? 'Chat' : t === 'status' ? 'Status' : 'Admin'}
            </button>
          ))}
          {onLogout && (
            <>
              <span className="ml-2 text-xs text-gray-500">{me.username}</span>
              <button
                onClick={onLogout}
                className="px-3 py-1 text-sm rounded-md text-gray-400 hover:text-gray-200 hover:bg-gray-800 transition-colors"
              >
                Logout
              </button>
            </>
          )}
        </nav>
      </header>

      <main className="flex-1 overflow-hidden">
        {tab === 'sessions' && <SessionList call={call} />}
        {tab === 'chat' && <ChatView call={call} />}
        {tab === 'status' && <StatusBar />}
        {tab === 'admin' && <AdminView call={call} pane={adminPane} onPane={setAdminPane} />}
      </main>
    </div>
  )
}

