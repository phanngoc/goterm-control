import { useState } from 'react'
import type { Me } from '../Root'

export default function LoginView({ onLogin }: { onLogin: (me: Me) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const res = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      if (res.ok) {
        const user = await res.json()
        onLogin({ enabled: true, ...user })
      } else if (res.status === 429) {
        setError('Too many attempts — try again in a minute')
      } else {
        setError('Invalid username or password')
      }
    } catch {
      setError('Cannot reach the gateway')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="h-full flex items-center justify-center bg-gray-950 text-gray-100">
      <form onSubmit={submit} className="w-80 p-8 bg-gray-900 rounded-xl border border-gray-800 space-y-4">
        <div className="text-center space-y-1">
          <h1 className="text-xl font-semibold tracking-tight">BomClaw</h1>
          <p className="text-sm text-gray-400">Sign in to the dashboard</p>
        </div>

        <input
          autoFocus
          value={username}
          onChange={e => setUsername(e.target.value)}
          placeholder="Username"
          autoComplete="username"
          className="w-full px-3 py-2 bg-gray-800 rounded-md border border-gray-700 focus:border-gray-500 focus:outline-none text-sm"
        />
        <input
          type="password"
          value={password}
          onChange={e => setPassword(e.target.value)}
          placeholder="Password"
          autoComplete="current-password"
          className="w-full px-3 py-2 bg-gray-800 rounded-md border border-gray-700 focus:border-gray-500 focus:outline-none text-sm"
        />

        {error && <p className="text-sm text-red-400">{error}</p>}

        <button
          type="submit"
          disabled={busy || !username || !password}
          className="w-full py-2 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium transition-colors"
        >
          {busy ? 'Signing in...' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
