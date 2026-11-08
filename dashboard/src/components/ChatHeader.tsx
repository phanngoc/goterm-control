import { useEffect, useState } from 'react'
import { useStore } from '../stores/store'
import { eventsToMessages } from '../lib/transcript'

interface AccountInfo {
  name: string
  sessions: number
  cooling_until?: string
  current?: boolean
}

// What the Sessions page used to be, cut down to what the Chat tab needs: which
// CLI session of the conversation is on screen, a fresh one, and a fresh one on
// another login. The conversation itself — Telegram and the dashboard as one —
// reads in Messages → Direct; this is for picking up an older context or
// starting clean.
export default function ChatHeader({ call }: { call: (m: string, p?: any) => Promise<any> }) {
  const [accounts, setAccounts] = useState<AccountInfo[]>([])
  useEffect(() => {
    call('accounts.list').then((a: AccountInfo[]) => setAccounts(a || [])).catch(() => {})
  }, [call])

  const sessions = [...useStore(s => s.sessions)].sort(
    (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
  )
  const activeSessionId = useStore(s => s.activeSessionId)
  const setActiveSessionId = useStore(s => s.setActiveSessionId)
  const setMessages = useStore(s => s.setMessages)

  const open = async (id: string) => {
    setActiveSessionId(id)
    setMessages([])
    try {
      const events = await call('transcript.get', { session_id: id })
      if (Array.isArray(events)) setMessages(eventsToMessages(events))
    } catch (e) {
      console.error('Failed to load transcript:', e)
    }
  }

  const startNew = () => {
    setActiveSessionId('chat_' + Date.now())
    setMessages([])
  }

  // An account belongs to a conversation, not to a setting on one: both CLIs
  // keep their conversation store inside the credential directory, so this
  // starts a new session rather than repointing a live one.
  const startOn = async (account: string) => {
    try {
      const r = await call('accounts.use', { account, chat_id: 0 })
      await call('sessions.list').then(useStore.getState().setSessions)
      setActiveSessionId(r.session_id)
      setMessages([])
    } catch (e: any) {
      alert(String(e?.message ?? e))
    }
  }

  const known = sessions.some(s => s.id === activeSessionId)

  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-gray-800 bg-gray-900/40">
      <select
        value={known ? activeSessionId ?? '' : ''}
        onChange={e => { if (e.target.value) open(e.target.value) }}
        title="Which session of this conversation to show"
        className="min-w-0 flex-1 max-w-md px-2 py-1.5 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none truncate"
      >
        {!known && <option value="">new session</option>}
        {sessions.map(s => (
          <option key={s.id} value={s.id}>
            {(s.label || s.id)} · {s.message_count} turns{s.account ? ` · ${s.account}` : ''}
          </option>
        ))}
      </select>
      {accounts.length > 1 && (
        <select
          value=""
          onChange={e => { if (e.target.value) startOn(e.target.value) }}
          title="Start a new conversation on another login"
          className="px-2 py-1.5 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-300 outline-none"
        >
          <option value="">new chat on…</option>
          {accounts.map(a => (
            <option key={a.name} value={a.name}>
              {a.name}{a.cooling_until ? ' (cooling)' : ''}
            </option>
          ))}
        </select>
      )}
      <button
        onClick={startNew}
        className="ml-auto px-3 py-1.5 text-sm bg-blue-600 text-white rounded-lg hover:bg-blue-500 transition-colors"
      >
        + New Chat
      </button>
    </div>
  )
}
