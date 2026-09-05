import { useEffect, useState } from 'react'
import type { Overview as OverviewData } from './types'
import Overview from './Overview'
import TraceExplorer from './TraceExplorer'
import TaskBoard from './TaskBoard'
import MessageStream from './MessageStream'

type Call = (method: string, params?: any) => Promise<any>
type Pane = 'overview' | 'traces' | 'tasks' | 'messages'

const PANES: { key: Pane; label: string }[] = [
  { key: 'overview', label: 'Overview' },
  { key: 'traces', label: 'Traces' },
  { key: 'tasks', label: 'Tasks' },
  { key: 'messages', label: 'Messages' },
]

export default function AdminView({ call }: { call: Call }) {
  const [pane, setPane] = useState<Pane>('overview')
  const [data, setData] = useState<OverviewData | null>(null)
  const [err, setErr] = useState<string | null>(null)

  // The overview drives the agent list every other pane filters by, so it is
  // refreshed regardless of which pane is showing.
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const d = await call('admin.overview')
        if (!cancelled) { setData(d); setErr(null) }
      } catch (e: any) {
        if (!cancelled) setErr(String(e?.message ?? e))
      }
    }
    load()
    const id = setInterval(load, 8000)
    return () => { cancelled = true; clearInterval(id) }
  }, [call])

  if (err) {
    return (
      <div className="p-6">
        <div className="rounded-lg bg-red-500/10 ring-1 ring-red-500/30 p-4">
          <div className="text-sm text-red-300 font-medium">Coordination unavailable</div>
          <div className="mt-1 text-xs text-red-300/80">{err}</div>
          <div className="mt-3 text-xs text-gray-400">
            Traces, tasks and inter-agent messages live in the shared database. Set
            <code className="mx-1 font-mono text-gray-300">coord.enabled: true</code>
            in the gateway config and restart it.
          </div>
        </div>
      </div>
    )
  }

  const agentIDs = data?.agents.map(a => a.id) ?? []
  const selfID = data?.agent_id ?? ''

  return (
    <div className="h-full flex flex-col">
      <div className="flex items-center gap-1 px-4 py-2 border-b border-gray-800 bg-gray-900/40">
        {PANES.map(p => (
          <button
            key={p.key}
            onClick={() => setPane(p.key)}
            className={`px-3 py-1 text-sm rounded-md transition-colors ${
              pane === p.key ? 'bg-gray-700 text-white' : 'text-gray-400 hover:text-gray-200 hover:bg-gray-800'
            }`}
          >
            {p.label}
            {p.key === 'tasks' && data && data.stats.open_tasks > 0 && (
              <span className="ml-1.5 text-[10px] px-1.5 py-0.5 rounded-full bg-amber-500/20 text-amber-300">
                {data.stats.open_tasks}
              </span>
            )}
            {p.key === 'messages' && data && data.stats.unread_messages > 0 && (
              <span className="ml-1.5 text-[10px] px-1.5 py-0.5 rounded-full bg-sky-500/20 text-sky-300">
                {data.stats.unread_messages}
              </span>
            )}
          </button>
        ))}
      </div>

      <div className="flex-1 min-h-0">
        {pane === 'overview' && <Overview data={data} />}
        {pane === 'traces' && <TraceExplorer call={call} agents={agentIDs} />}
        {pane === 'tasks' && <TaskBoard call={call} agents={agentIDs} />}
        {pane === 'messages' && <MessageStream call={call} agents={agentIDs} selfID={selfID} />}
      </div>
    </div>
  )
}
