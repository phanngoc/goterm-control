import type { Overview as OverviewData } from './types'
import { ago, ms, tokens } from './format'

function Tile({ label, value, hint, accent }: {
  label: string; value: string; hint?: string; accent?: string
}) {
  return (
    <div className="rounded-lg bg-gray-900 ring-1 ring-gray-800 px-4 py-3">
      <div className="text-[11px] uppercase tracking-wider text-gray-500">{label}</div>
      <div className={`mt-1 text-2xl font-semibold tabular-nums ${accent ?? 'text-gray-100'}`}>{value}</div>
      {hint && <div className="mt-0.5 text-xs text-gray-500">{hint}</div>}
    </div>
  )
}

function AgentCard({ agent, isSelf }: { agent: OverviewData['agents'][number]; isSelf: boolean }) {
  return (
    <div className="rounded-lg bg-gray-900 ring-1 ring-gray-800 p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="relative flex h-2.5 w-2.5">
              {agent.online && (
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
              )}
              <span className={`relative inline-flex h-2.5 w-2.5 rounded-full ${agent.online ? 'bg-emerald-400' : 'bg-gray-600'}`} />
            </span>
            <span className="font-medium text-gray-100 truncate">{agent.display_name || agent.id}</span>
            {isSelf && (
              <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-400">this gateway</span>
            )}
          </div>
          <div className="mt-0.5 font-mono text-xs text-gray-500 truncate">{agent.id}</div>
        </div>
        <div className="text-right shrink-0">
          <div className={`text-xs font-medium ${agent.online ? 'text-emerald-400' : 'text-gray-500'}`}>
            {agent.online ? 'online' : 'offline'}
          </div>
          <div className="text-[11px] text-gray-600">{ago(agent.last_seen_at)}</div>
        </div>
      </div>

      <div className="mt-3 flex flex-wrap gap-1.5">
        {agent.provider && (
          <span className="text-[11px] px-2 py-0.5 rounded ring-1 ring-sky-500/30 bg-sky-500/10 text-sky-300">
            {agent.provider}
          </span>
        )}
        {agent.model && (
          <span className="text-[11px] px-2 py-0.5 rounded ring-1 ring-gray-700 text-gray-400 font-mono">
            {agent.model}
          </span>
        )}
      </div>

      <dl className="mt-3 space-y-1 text-xs">
        {agent.workspace && (
          <div className="flex gap-2">
            <dt className="text-gray-600 w-20 shrink-0">workspace</dt>
            <dd className="text-gray-400 font-mono truncate">{agent.workspace}</dd>
          </div>
        )}
        {agent.ws_addr && (
          <div className="flex gap-2">
            <dt className="text-gray-600 w-20 shrink-0">address</dt>
            <dd className="text-gray-400 font-mono truncate">{agent.ws_addr}</dd>
          </div>
        )}
        <div className="flex gap-2">
          <dt className="text-gray-600 w-20 shrink-0">started</dt>
          <dd className="text-gray-400">{ago(agent.started_at)}</dd>
        </div>
      </dl>
    </div>
  )
}

export default function Overview({ data }: { data: OverviewData | null }) {
  if (!data) {
    return <div className="p-6 text-sm text-gray-500">Loading…</div>
  }
  const s = data.stats
  const errorRate = s.traces_24h > 0 ? (s.errors_24h / s.traces_24h) * 100 : 0

  return (
    <div className="p-6 space-y-6 overflow-y-auto h-full">
      <section>
        <h2 className="text-xs uppercase tracking-wider text-gray-500 mb-3">Last 24 hours</h2>
        <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
          <Tile label="Turns" value={String(s.traces_24h)} hint="traced end to end" />
          <Tile
            label="Errors"
            value={String(s.errors_24h)}
            hint={s.traces_24h ? `${errorRate.toFixed(1)}% of spans` : 'nothing ran'}
            accent={s.errors_24h > 0 ? 'text-red-400' : 'text-gray-100'}
          />
          <Tile label="Tool calls" value={String(s.tool_calls_24h)} hint="across all agents" />
          <Tile label="Tokens" value={tokens(s.tokens_24h)} hint="input + output" />
          <Tile label="Avg turn" value={ms(s.avg_duration_ms)} hint="root run duration" />
          <Tile
            label="Open tasks"
            value={String(s.open_tasks)}
            hint={`${s.unread_messages} unread message${s.unread_messages === 1 ? '' : 's'}`}
            accent={s.open_tasks > 0 ? 'text-amber-300' : 'text-gray-100'}
          />
        </div>
      </section>

      <section>
        <h2 className="text-xs uppercase tracking-wider text-gray-500 mb-3">
          Agents ({data.agents.filter(a => a.online).length}/{data.agents.length} online)
        </h2>
        {data.agents.length === 0 ? (
          <div className="rounded-lg bg-gray-900 ring-1 ring-gray-800 p-6 text-sm text-gray-500">
            No agent has registered yet. A gateway registers itself on startup when
            coordination is enabled.
          </div>
        ) : (
          <div className="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-3">
            {data.agents.map(a => (
              <AgentCard key={a.id} agent={a} isSelf={a.id === data.agent_id} />
            ))}
          </div>
        )}
      </section>

      <section>
        <h2 className="text-xs uppercase tracking-wider text-gray-500 mb-3">Task queue</h2>
        <div className="rounded-lg bg-gray-900 ring-1 ring-gray-800 p-4">
          {Object.keys(s.task_counts).length === 0 ? (
            <div className="text-sm text-gray-500">No tasks yet.</div>
          ) : (
            <div className="flex flex-wrap gap-4">
              {Object.entries(s.task_counts).map(([state, n]) => (
                <div key={state}>
                  <div className="text-lg font-semibold tabular-nums text-gray-100">{n}</div>
                  <div className="text-xs text-gray-500">{state}</div>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      <p className="text-[11px] text-gray-600 font-mono">{data.db_path}</p>
    </div>
  )
}
