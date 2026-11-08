import { lazy, Suspense, useEffect, useState } from 'react'
import { useStore } from './stores/store'
import { useGateway } from './hooks/useGateway'
import AdminView from './admin/AdminView'
import type { Me } from './Root'
import { parseRoute, pathFor, isRefinement, lineFromHash, type AdminPane } from './lib/route'

// Monaco is several MB; only the editor's own page and dialog load it.
const ProjectEditor = lazy(() => import('./admin/ProjectEditor'))

export default function App({ me, onLogout }: { me: Me; onLogout?: () => void }) {
  const { call } = useGateway()
  const tab = useStore(s => s.tab)
  const setTab = useStore(s => s.setTab)
  const connected = useStore(s => s.connected)
  const setSessions = useStore(s => s.setSessions)
  const setStatus = useStore(s => s.setStatus)

  // Which admin pane is on screen. Lifted out of AdminView so that one place
  // owns the address bar; AdminView is now told which pane to show.
  const [adminPane, setAdminPane] = useState<AdminPane>('messages')
  // Which task's detail is open, mirrored in the address bar. A drawer that
  // lives only in component state cannot be sent to anybody — "look at this
  // task" meant "open the board and find it".
  const [adminTask, setAdminTask] = useState<string>('')
  // Which room the Messages pane is showing, for the same reason as the task
  // above: one address per room, so a room can be linked and reloaded.
  const [adminChannel, setAdminChannel] = useState<string>('')
  // The editor on a page of its own: /files/<channel>/<path>#L<line>.
  const [filesRoute, setFilesRoute] = useState<{ channelId: string; filePath?: string; line?: number; preview?: boolean } | null>(null)
  const [filesRoot, setFilesRoot] = useState('')

  // Sync URL → state, on load and whenever the browser navigates. The popstate
  // half is new: without it Back and Forward changed the address bar and left
  // the view where it was.
  useEffect(() => {
    const apply = () => {
      const r = parseRoute(location.pathname)
      setTab(r.tab)
      if (r.adminPane) setAdminPane(r.adminPane)
      setAdminTask(r.taskId ?? '')
      if (r.tab === 'files') {
        setFilesRoute({
          channelId: r.channelId!, filePath: r.filePath, line: lineFromHash(location.hash),
          preview: new URLSearchParams(location.search).has('preview'),
        })
        return
      }
      // Only when the address names one. A bare /admin/messages leaves the
      // pane's own pick alone rather than blanking the room it just chose.
      if (r.channelId) setAdminChannel(r.channelId)
    }
    apply()
    window.addEventListener('popstate', apply)
    return () => window.removeEventListener('popstate', apply)
  }, [setTab])

  // State → URL. pushState rather than replaceState, so moving between tabs
  // leaves history to go back through; and only when the path actually differs,
  // which keeps the effect from stacking an entry per render (including the one
  // right after the read above).
  //
  // The exception is filling in a detail already on screen — /admin/messages
  // becoming /admin/messages/<id> when the busiest room is auto-selected. That
  // is the same navigation, not a second one (see isRefinement).
  useEffect(() => {
    // The editor page keeps its own address current (file and line); this
    // effect knowing nothing of either would only undo that.
    // The store, not only `tab`: on first load this effect runs in the same
    // commit as the read above, with `tab` still the default.
    if (tab === 'files' || useStore.getState().tab === 'files') return
    const path = pathFor({
      tab, adminPane,
      taskId: adminTask || undefined,
      channelId: adminChannel || undefined,
    })
    const here = location.pathname
    if (path === here) return
    // The bare address is only ever a way in to Messages, so it is replaced
    // rather than left behind for Back to bounce off.
    history[here === '/' || isRefinement(here, path) ? 'replaceState' : 'pushState'](null, '', path)
  }, [tab, adminPane, adminTask, adminChannel])

  // This gateway's status and session totals, for Status in the sidebar.
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
      } catch {}
    }

    loadAll()
    const interval = setInterval(() => {
      call('sessions.list').then((s: any) => setSessions(s || [])).catch(() => {})
      call('status').then(setStatus).catch(() => {})
    }, 10_000)
    return () => clearInterval(interval)
  }, [connected, call, setSessions, setStatus])

  // The editor page needs the project's folder, for its title bar.
  useEffect(() => {
    if (tab !== 'files' || !filesRoute || !connected) return
    call('channels.list')
      .then((cs: any) => setFilesRoot((cs || []).find((c: any) => c.id === filesRoute.channelId)?.workspace ?? ''))
      .catch(() => {})
  }, [tab, filesRoute, connected, call])

  const goMessages = () => { setTab('admin'); setAdminPane('messages') }

  if (tab === 'files' && filesRoute) {
    return (
      <Suspense fallback={<div className="h-full flex items-center justify-center bg-[#1e1e1e] text-sm text-gray-500">Đang mở editor…</div>}>
        <ProjectEditor
          key={filesRoute.channelId}
          call={call} channelID={filesRoute.channelId} root={filesRoot}
          standalone initialFile={filesRoute.filePath} initialLine={filesRoute.line} initialPreview={filesRoute.preview}
          onClose={() => {
            setAdminChannel(filesRoute.channelId)
            setAdminPane('messages')
            setTab('admin')
          }}
        />
      </Suspense>
    )
  }

  // No top bar: the name and connection head the sidebar, and who is signed in
  // closes it. Everything else is a room, a thread, or a tool in that sidebar.
  const brand = (
    <div className="flex items-center gap-2 px-3 py-2.5 border-b border-gray-800">
      <button onClick={goMessages} className="text-base font-semibold tracking-tight text-gray-100 hover:text-white">
        BomClaw
      </button>
      <span
        className={`w-2 h-2 rounded-full ${connected ? 'bg-green-400' : 'bg-red-400'}`}
        title={connected ? 'đã kết nối gateway' : 'mất kết nối gateway'}
      />
    </div>
  )
  const footer = onLogout ? (
    <div className="flex items-center gap-2 px-3 py-2 border-t border-gray-800 text-xs">
      <span className="truncate text-gray-500">{me.username}</span>
      <button onClick={onLogout} className="ml-auto text-gray-500 hover:text-gray-200">Logout</button>
    </div>
  ) : undefined

  return (
    <div className="h-full bg-gray-950 text-gray-100">
      <AdminView
        call={call} pane={adminPane} onPane={setAdminPane}
        taskId={adminTask} onTaskId={setAdminTask}
        channelId={adminChannel} onChannelId={setAdminChannel}
        brand={brand} footer={footer}
      />
    </div>
  )
}
