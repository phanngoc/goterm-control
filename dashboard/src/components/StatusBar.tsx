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

function StatCard({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div className="bg-gray-900 rounded-lg border border-gray-800 p-3">
      <div className="text-xs text-gray-500">{label}</div>
      <div className={`text-lg font-semibold mt-0.5 ${color || 'text-gray-200'}`}>{value}</div>
    </div>
  )
}
