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
export type AdminPane = 'overview' | 'traces' | 'tasks' | 'schedules' | 'notes' | 'messages'

export const ADMIN_PANES: AdminPane[] = ['overview', 'traces', 'tasks', 'schedules', 'notes', 'messages']

export interface Route {
  tab: Tab
  sessionId?: string
  adminPane?: AdminPane
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
      return `/admin/${r.adminPane ?? 'overview'}`
    case 'sessions':
      return '/'
  }
}
