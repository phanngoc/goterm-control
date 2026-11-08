import { useStore } from '../stores/store'

export default function StatusBar() {
  const status = useStore(s => s.status)
  const connected = useStore(s => s.connected)
  const sessions = useStore(s => s.sessions)

  const totalTokens = sessions.reduce((sum, s) => sum + s.input_tokens + s.output_tokens, 0)
  const totalTurns = sessions.reduce((sum, s) => sum + s.message_count, 0)

  return (
    <div className="h-full overflow-y-auto p-4">
      <h2 className="text-lg font-medium text-gray-300 mb-4">Status</h2>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mb-6">
        <StatCard
          label="Gateway"
          value={connected ? 'Online' : 'Offline'}
          color={connected ? 'text-green-400' : 'text-red-400'}
        />
        <StatCard label="Uptime" value={status?.uptime || '—'} />
        <StatCard label="Sessions" value={String(sessions.length)} />
        <StatCard label="Model" value={status?.default_model || '—'} />
      </div>

      <div className="grid grid-cols-2 gap-3 mb-6">
        <StatCard label="Total Turns" value={String(totalTurns)} />
        <StatCard label="Total Tokens" value={totalTokens.toLocaleString()} />
      </div>

      <BrowserBridge browser={status?.browser} />

      <h3 className="text-sm font-medium text-gray-400 mb-2">Channels</h3>
      <div className="flex gap-2">
        {(status?.channels || []).map((ch: string) => (
          <span key={ch} className="px-2 py-1 bg-gray-800 rounded text-xs text-gray-300">
            {ch}
          </span>
        ))}
      </div>

      <h3 className="text-sm font-medium text-gray-400 mt-6 mb-2">Sessions Detail</h3>
      <div className="overflow-x-auto">
        <table className="w-full text-sm text-left">
          <thead className="text-xs text-gray-500 border-b border-gray-800">
            <tr>
              <th className="py-2 pr-4">ID</th>
              <th className="py-2 pr-4">Turns</th>
              <th className="py-2 pr-4">Tokens</th>
              <th className="py-2">Updated</th>
            </tr>
          </thead>
          <tbody>
            {sessions.map(s => (
              <tr key={s.id} className="border-b border-gray-800/50">
                <td className="py-2 pr-4 font-mono text-gray-300">{s.id}</td>
                <td className="py-2 pr-4 text-gray-400">{s.message_count}</td>
                <td className="py-2 pr-4 text-gray-400">{(s.input_tokens + s.output_tokens).toLocaleString()}</td>
                <td className="py-2 text-gray-500">{new Date(s.updated_at).toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// BrowserBridge reports the Chrome extension that lets this agent drive the
// user's own logged-in browser. Absent when the bridge is disabled in config;
// "not connected" is a normal state, not a fault, so it reads as a status
// rather than an error.
function BrowserBridge({ browser }: { browser?: any }) {
  if (!browser) return null
  const on = !!browser.connected

  return (
    <>
      <h3 className="text-sm font-medium text-gray-400 mb-2">Browser Bridge</h3>
      <div className="bg-gray-900 rounded-lg border border-gray-800 p-3 mb-6">
        <div className="flex items-center gap-2">
          <span className={`w-2 h-2 rounded-full ${on ? 'bg-green-400' : 'bg-gray-600'}`} />
          <span className={`text-sm ${on ? 'text-gray-200' : 'text-gray-500'}`}>
            {on ? browser.browser_name || 'browser connected' : 'no browser connected'}
          </span>
          {browser.agent_name && (
            <span className="text-xs text-gray-500 ml-auto">{browser.agent_name}</span>
          )}
        </div>

        {on ? (
          <div className="text-xs text-gray-500 mt-2 space-y-0.5">
            <div>
              {browser.actions ?? 0} action{browser.actions === 1 ? '' : 's'}
              {browser.last_action && <> · last: <span className="text-gray-400">{browser.last_action}</span></>}
              {browser.last_action_at && <> · {new Date(browser.last_action_at).toLocaleTimeString()}</>}
            </div>
            {browser.connected_at && (
              <div>connected since {new Date(browser.connected_at).toLocaleTimeString()}</div>
            )}
            {browser.last_error && (
              <div className="text-red-400/80">last error: {browser.last_error}</div>
            )}
          </div>
        ) : (
          <div className="text-xs text-gray-500 mt-2">
            Pair the BomClaw Browser Bridge extension with{' '}
            <code className="text-gray-400">bomclaw browser token</code> to let this agent
            work in your logged-in tabs.
          </div>
        )}
      </div>
    </>
  )
}

function StatCard({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div className="bg-gray-900 rounded-lg border border-gray-800 p-3">
      <div className="text-xs text-gray-500">{label}</div>
      <div className={`text-lg font-semibold mt-0.5 ${color || 'text-gray-200'}`}>{value}</div>
    </div>
  )
}
