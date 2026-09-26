// One address per view, so a pane can be linked, bookmarked and reloaded.
//
// The gateway already serves index.html for any path that is not a real file,
// so every route below survives a hard refresh with no server change. What was
// missing was the client half: the admin panes lived in component state and
// never reached the address bar, so "send me the traces tab" meant "open /admin
// and then click Traces".
//
// Kept as pure functions on purpose — routing is the kind of thing that is
// easier to test than to debug through a browser.

export type Tab = 'sessions' | 'chat' | 'status' | 'admin'
export type AdminPane = 'overview' | 'traces' | 'tasks' | 'schedules' | 'notes' | 'messages' | 'skills' | 'settings'

export const ADMIN_PANES: AdminPane[] = ['overview', 'traces', 'tasks', 'schedules', 'notes', 'messages', 'skills', 'settings']

export interface Route {
  tab: Tab
  sessionId?: string
  adminPane?: AdminPane
  /** taskId opens that task's detail on the Tasks pane: /admin/tasks/<id>.
   *  A drawer that lives only in component state cannot be sent to anybody —
   *  "look at this task" meant "open the board and find it". */
  taskId?: string
  /** channelId opens that room on the Messages pane: /admin/messages/<id>.
   *  Same reason as taskId, and the same failure without it: every room in
   *  the sidebar shared one address, so "read what they said in #Trading"
   *  meant "open Messages and click around until you find it". */
  channelId?: string
}

/** parseRoute reads a pathname into the view it names. */
export function parseRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean)

  switch (parts[0]) {
    case 'chat':
      // /chat/<id> opens that session; bare /chat is the composer.
      return parts[1] ? { tab: 'chat', sessionId: decodeURIComponent(parts[1]) } : { tab: 'chat' }

    case 'status':
      return { tab: 'status' }

    case 'admin': {
      // An unknown or missing pane lands on Overview rather than a blank panel,
      // which is what a stale or hand-typed link deserves.
      const pane = ADMIN_PANES.find(p => p === parts[1])
      if (pane === 'tasks' && parts[2]) {
        return { tab: 'admin', adminPane: 'tasks', taskId: decodeURIComponent(parts[2]) }
      }
      if (pane === 'messages' && parts[2]) {
        return { tab: 'admin', adminPane: 'messages', channelId: decodeURIComponent(parts[2]) }
      }
      return { tab: 'admin', adminPane: pane ?? 'overview' }
    }

    default:
      return { tab: 'sessions' }
  }
}

/** pathFor is parseRoute's inverse: the address a view should be showing. */
export function pathFor(r: Route): string {
  switch (r.tab) {
    case 'chat':
      // "new" is the composer's placeholder id, not a session that exists.
      return r.sessionId && r.sessionId !== 'new' ? `/chat/${encodeURIComponent(r.sessionId)}` : '/chat'
    case 'status':
      return '/status'
    case 'admin':
      if (r.adminPane === 'tasks' && r.taskId) return `/admin/tasks/${encodeURIComponent(r.taskId)}`
      if (r.adminPane === 'messages' && r.channelId) return `/admin/messages/${encodeURIComponent(r.channelId)}`
      return `/admin/${r.adminPane ?? 'overview'}`
    case 'sessions':
      return '/'
  }
}

/** isRefinement says whether moving from `here` to `path` is the same
 *  navigation finishing rather than a second one — the difference between
 *  replaceState and pushState.
 *
 *  It exists because getting this wrong is invisible until someone presses
 *  Back. A view that auto-selects something on arrival (the newest session,
 *  the busiest room) turns one click into two addresses; pushing the second
 *  leaves an entry that Back returns to and the auto-select immediately
 *  leaves again, so Back appears to do nothing.
 *
 *  Opening a task is the opposite: nothing opens a task on its own, so it
 *  earns a history entry and Back closes the drawer instead of leaving the
 *  admin tab. Switching rooms is not a prefix of the room you were in, so it
 *  pushes without needing a rule of its own.
 */
export function isRefinement(here: string, path: string): boolean {
  if (here === '/' || !path.startsWith(here + '/')) return false
  if (!here.startsWith('/admin')) return true // /chat, which this was written for
  return here === '/admin/messages'
}
