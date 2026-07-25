import { useEffect, useState } from 'react'
import App from './App'
import LoginView from './components/LoginView'

export interface Me {
  enabled: boolean
  username: string
}

// Root gates the whole app behind /api/me: the WebSocket only connects
// (App only mounts) once there is a valid login session — or when the
// gateway reports auth is disabled (local-only setups).
export default function Root() {
  const [me, setMe] = useState<Me | null | undefined>(undefined)

  useEffect(() => {
    fetch('/api/me')
      .then(async r => setMe(r.ok ? await r.json() : null))
      .catch(() => setMe(null))
  }, [])

  const logout = async () => {
    try { await fetch('/api/logout', { method: 'POST' }) } catch {}
    setMe(null)
  }

  if (me === undefined) {
    return <div className="h-full flex items-center justify-center bg-gray-950 text-gray-500 text-sm">Loading...</div>
  }
  if (me === null) {
    return <LoginView onLogin={setMe} />
  }
  return <App me={me} onLogout={me.enabled ? logout : undefined} />
}
