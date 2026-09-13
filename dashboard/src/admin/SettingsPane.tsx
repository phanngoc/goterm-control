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
  const [data, setData] = useState<AgentSettings | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [picked, setPicked] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const d: AgentSettings = await call('admin.settings')
      setData(d)
      setPicked(p => p || d.model)
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => { load() }, [load])

  const apply = async (force: boolean) => {
    if (!data || !picked || picked === data.model) return
    setBusy(true)
    setNote(null)
    try {
      const r = await call('admin.set_model', { model: picked, ...(force ? { force: true } : {}) })
      setErr(null)
      setNote(r?.restarted
        ? `Đã ghi config và khởi động lại ${data.agent_id}. Đợi vài giây rồi tải lại trang.`
        : (r?.note ?? 'Đã ghi config.'))
      // The gateway is restarting under us; give it a moment, then re-read.
      setTimeout(load, 6000)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  if (err && !data) return <div className="p-4 text-sm text-red-300">{err}</div>
  if (!data) return <div className="p-4 text-sm text-gray-500">Loading…</div>

  const changed = picked !== data.model
  const target = data.choices.find(c => c.id === picked)

  return (
    <div className="p-4 max-w-2xl space-y-4">
      <div>
        <h2 className="text-sm text-gray-200 font-medium">{data.agent_name || data.agent_id}</h2>
        <p className="text-xs text-gray-500 font-mono">{data.config_path}</p>
      </div>

      {err && <div className="text-xs text-red-300">{err}</div>}
      {note && <div className="text-xs text-emerald-300">{note}</div>}

      <dl className="grid grid-cols-2 gap-2 text-xs">
        <div><dt className="text-gray-600">backend</dt><dd className="font-mono text-gray-200">{data.provider}</dd></div>
        <div><dt className="text-gray-600">model</dt><dd className="font-mono text-gray-200">{data.model}</dd></div>
      </dl>

      <div className="space-y-2">
        <label className="block text-xs text-gray-500">
          Đổi model — backend đi theo model, không chọn riêng
        </label>
        <select
          value={picked}
          onChange={e => setPicked(e.target.value)}
          className="w-full px-2 py-2 text-sm bg-gray-950 rounded ring-1 ring-gray-800 text-gray-200 outline-none"
        >
          {data.choices.map(c => (
            <option key={c.id} value={c.id}>
              {c.name} — {c.provider} ({c.id})
            </option>
          ))}
        </select>
        {changed && target && (
          <p className="text-xs text-amber-300">
            {data.provider === target.provider
              ? `Cùng backend ${target.provider}, chỉ đổi model.`
              : `Đổi backend ${data.provider} → ${target.provider}.`}
            {' '}Agent sẽ khởi động lại{data.busy ? ' — và đang có việc chạy dở, nó sẽ bị cắt.' : '.'}
          </p>
        )}
      </div>

      <div className="flex items-center gap-2">
        <button
          onClick={() => apply(false)}
          disabled={!changed || busy || !data.can_restart}
          className="px-3 py-2 text-sm rounded bg-gray-100 text-gray-900 font-medium hover:bg-white disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {busy ? 'Đang đổi…' : 'Đổi & khởi động lại'}
        </button>
        {data.busy && changed && (
          <button
            onClick={() => apply(true)}
            disabled={busy}
            className="px-3 py-2 text-sm rounded ring-1 ring-amber-500/50 text-amber-300 hover:bg-amber-500/10"
          >
            Cắt việc đang chạy và đổi
          </button>
        )}
        {!data.can_restart && (
          <span className="text-xs text-gray-500">
            Gateway này không chạy dưới service manager — đổi xong phải tự restart.
          </span>
        )}
      </div>

      <p className="text-xs text-gray-600">
        Chỉ agent này. Mỗi gateway đọc file config của chính nó, nên đổi backend cho agent khác
        thì mở dashboard của agent đó.
      </p>
    </div>
  )
}
