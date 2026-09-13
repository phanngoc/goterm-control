import { useCallback, useEffect, useState } from 'react'

type Call = (method: string, params?: any) => Promise<any>

interface ModelChoice {
  id: string
  name: string
  api: string
  provider: string
  current: boolean
}

interface AgentSettings {
  agent_id: string
  agent_name: string
  provider: string
  model: string
  config_path: string
  choices: ModelChoice[]
  can_restart: boolean
  busy: boolean
  reachable: boolean
  error?: string
}

// Which backend this agent runs on, and changing it.
//
// The choice is a MODEL, not a provider: the model's API is what selects the
// backend, so picking the model and letting the provider follow means the two
// cannot be set to disagree — which is exactly the mistake this screen would
// otherwise make easy.
//
// Only this agent's own settings. A gateway can read the shared database but
// not another agent's config file, and a screen that pretended otherwise would
// be editing a file nobody reloads.
export default function SettingsPane({ call }: { call: Call }) {
  const [agents, setAgents] = useState<AgentSettings[] | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setAgents((await call('admin.settings')) || [])
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => { load() }, [load])

  if (err && !agents) return <div className="p-4 text-sm text-red-300">{err}</div>
  if (!agents) return <div className="p-4 text-sm text-gray-500">Loading…</div>

  return (
    <div className="p-4 max-w-3xl space-y-4">
      {err && <div className="text-xs text-red-300">{err}</div>}
      {agents.map(a => <AgentCard key={a.agent_id} agent={a} call={call} onChanged={load} />)}
      <p className="text-xs text-gray-600">
        Mỗi gateway đọc file config của chính nó; những agent khác được hỏi qua loopback,
        nên một agent đang tắt sẽ hiện là không liên lạc được chứ không biến mất khỏi danh sách.
      </p>
    </div>
  )
}

function AgentCard({ agent, call, onChanged }: { agent: AgentSettings; call: Call; onChanged: () => void }) {
  const [picked, setPicked] = useState(agent.model)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => { setPicked(agent.model) }, [agent.model])

  const apply = async (force: boolean) => {
    if (!picked || picked === agent.model) return
    setBusy(true)
    setNote(null)
    try {
      const r = await call('admin.set_model', {
        agent_id: agent.agent_id, model: picked, ...(force ? { force: true } : {}),
      })
      setErr(null)
      setNote(r?.restarted
        ? `Đã ghi config và khởi động lại ${agent.agent_id}.`
        : (r?.note ?? 'Đã ghi config.'))
      setTimeout(onChanged, 6000)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  if (!agent.reachable) {
    return (
      <div className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
        <div className="flex items-baseline gap-2">
          <span className="text-sm text-gray-300">{agent.agent_name || agent.agent_id}</span>
          <span className="text-xs text-amber-300">không liên lạc được</span>
        </div>
        {agent.error && <p className="mt-1 text-xs text-gray-500 font-mono truncate">{agent.error}</p>}
      </div>
    )
  }

  const changed = picked !== agent.model
  const target = agent.choices?.find(c => c.id === picked)

  return (
    <div className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3 space-y-3">
      <div className="flex items-baseline gap-2 flex-wrap">
        <span className="text-sm text-gray-200 font-medium">{agent.agent_name || agent.agent_id}</span>
        <span className="text-xs font-mono px-1.5 rounded bg-sky-500/10 text-sky-300 ring-1 ring-sky-500/30">
          {agent.provider}
        </span>
        <span className="text-xs text-gray-500 font-mono">{agent.model}</span>
        {agent.busy && <span className="text-xs text-amber-300">đang chạy việc</span>}
        <span className="ml-auto text-[11px] text-gray-600 font-mono truncate">{agent.config_path}</span>
      </div>

      {err && <div className="text-xs text-red-300">{err}</div>}
      {note && <div className="text-xs text-emerald-300">{note}</div>}

      <div className="flex items-center gap-2 flex-wrap">
        <select
          value={picked}
          onChange={e => setPicked(e.target.value)}
          className="flex-1 min-w-0 px-2 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none"
        >
          {(agent.choices ?? []).map(c => (
            <option key={c.id} value={c.id}>{c.name} — {c.provider} ({c.id})</option>
          ))}
        </select>
        <button
          onClick={() => apply(false)}
          disabled={!changed || busy || !agent.can_restart}
          className="px-3 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {busy ? 'Đang đổi…' : 'Đổi & restart'}
        </button>
        {agent.busy && changed && (
          <button
            onClick={() => apply(true)}
            disabled={busy}
            className="px-3 py-2 text-sm rounded ring-1 ring-amber-500/50 text-amber-300 hover:bg-amber-500/10"
          >
            Cắt việc đang chạy
          </button>
        )}
      </div>

      {changed && target && (
        <p className="text-xs text-amber-300">
          {agent.provider === target.provider
            ? `Cùng backend ${target.provider}, chỉ đổi model.`
            : `Đổi backend ${agent.provider} → ${target.provider}.`}
          {' '}Agent sẽ khởi động lại{agent.busy ? ' — việc đang chạy sẽ bị cắt.' : '.'}
        </p>
      )}
      {!agent.can_restart && (
        <p className="text-xs text-gray-500">Không chạy dưới service manager — đổi xong phải tự restart.</p>
      )}
    </div>
  )
}
