import { useCallback, useEffect, useState } from 'react'
import { tokens } from './format'

type Call = (method: string, params?: any) => Promise<any>

interface AgentEcon {
  agent_id: string
  turns: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  task_cost_usd: number
  chat_cost_usd: number
  completed: number
  failed: number
  rejected: number
  canceled: number
  open: number
  runs: number
  verified: number
  passed: number
  mean_score: number
  scored: number
  judged: number
}

interface ProjectEcon {
  channel_id: string
  tasks: number
  completed: number
  cost_usd: number
  mean_score: number
}

interface Econ {
  since: string
  judge: string
  agents: AgentEcon[]
  projects: ProjectEcon[]
}

const usd = (n: number) => (n >= 100 ? `$${n.toFixed(0)}` : `$${n.toFixed(2)}`)
const pct = (n: number, d: number) => (d > 0 ? `${Math.round((100 * n) / d)}%` : '—')
const total = (a: AgentEcon) => a.input_tokens + a.output_tokens + a.cache_read_tokens + a.cache_write_tokens

// EconomicsPane is the agents' ledger, the page behind `bomclaw econ`: what
// each agent burns, what it gets done, and how the judge — one fixed third
// agent — grades the result. Sorted by spend, because the question it answers
// first is "where is the money going, and is it buying anything".
export default function EconomicsPane({ call, agents }: { call: Call; agents: string[] }) {
  const [days, setDays] = useState(7)
  const [data, setData] = useState<Econ | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    try {
      setData(await call('economics.get', { days }))
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call, days])
  useEffect(() => { load() }, [load])
  useEffect(() => {
    const t = setInterval(load, 30000)
    return () => clearInterval(t)
  }, [load])

  const setJudge = async (id: string) => {
    setSaving(true)
    try {
      await call('economics.judge', { agent_id: id })
      await load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setSaving(false)
    }
  }

  const rows = data?.agents ?? []
  const sum = rows.reduce((s, a) => ({
    cost: s.cost + a.cost_usd,
    done: s.done + a.completed,
    ended: s.ended + a.completed + a.failed + a.rejected,
    tok: s.tok + total(a),
    verified: s.verified + a.verified,
    passed: s.passed + a.passed,
  }), { cost: 0, done: 0, ended: 0, tok: 0, verified: 0, passed: 0 })

  return (
    <div className="h-full overflow-y-auto p-4 sm:p-6 space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-lg font-semibold text-gray-100">Agent economics</h2>
        <div className="flex rounded-md ring-1 ring-gray-700 overflow-hidden text-xs">
          {[1, 7, 30].map(d => (
            <button key={d} onClick={() => setDays(d)}
              className={`px-3 py-1 ${days === d ? 'bg-gray-100 text-gray-900' : 'text-gray-300 hover:bg-gray-800'}`}>
              {d === 1 ? '24h' : `${d} ngày`}
            </button>
          ))}
        </div>
        <label className="ml-auto flex items-center gap-2 text-xs text-gray-400">
          Giám khảo
          <select
            value={data?.judge ?? ''} disabled={saving || !data}
            onChange={e => setJudge(e.target.value)}
            className="h-7 rounded bg-gray-900 px-2 text-gray-100 ring-1 ring-gray-700 outline-none"
            title="Agent chấm điểm mọi goal đã xong mà nó không làm"
          >
            <option value="">bất kỳ peer nào</option>
            {agents.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </label>
      </div>

      {err && <div className="rounded bg-red-950/60 px-3 py-2 text-sm text-red-200">{err}</div>}

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <Tile label="Chi phí" value={usd(sum.cost)} hint="giá API niêm yết, theo CLI" />
        <Tile label="Token" value={tokens(sum.tok)} hint="input + output + cache" />
        <Tile label="Hoàn thành" value={pct(sum.done, sum.ended)} hint={`${sum.done}/${sum.ended} task kết thúc`} />
        <Tile label="Qua giám khảo" value={pct(sum.passed, sum.verified)} hint={`${sum.passed}/${sum.verified} goal đã chấm`} />
      </div>

      <section>
        <h3 className="mb-2 text-xs uppercase tracking-wider text-gray-500">Theo agent</h3>
        <div className="overflow-x-auto rounded-lg ring-1 ring-gray-800">
          <table className="min-w-full text-sm tabular-nums">
            <thead className="bg-gray-900/80 text-[11px] uppercase tracking-wide text-gray-400">
              <tr>
                {['Agent', 'Lượt', 'Token', 'Cache hit', 'Chi phí', 'Cho task', 'Xong', 'Hỏng', 'Bị trả', 'Huỷ', 'Đang mở', 'Hoàn thành', '$ / task', 'Điểm', 'Qua', 'Đã chấm hộ'].map(h => (
                  <th key={h} className="px-3 py-2 text-right first:text-left whitespace-nowrap">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-800">
              {rows.length === 0 && (
                <tr><td colSpan={16} className="px-3 py-6 text-center text-gray-500">Chưa có dữ liệu trong khoảng này.</td></tr>
              )}
              {rows.map(a => {
                const ended = a.completed + a.failed + a.rejected
                const fresh = a.input_tokens + a.cache_read_tokens + a.cache_write_tokens
                return (
                  <tr key={a.agent_id} className="text-gray-200">
                    <td className="px-3 py-2 text-left font-mono whitespace-nowrap">
                      {a.agent_id}{data?.judge === a.agent_id && <span className="ml-1.5 rounded bg-amber-500/15 px-1.5 text-[10px] text-amber-300">giám khảo</span>}
                    </td>
                    <td className="px-3 py-2 text-right">{a.turns}</td>
                    <td className="px-3 py-2 text-right">{tokens(total(a))}</td>
                    <td className="px-3 py-2 text-right">{pct(a.cache_read_tokens, fresh)}</td>
                    <td className="px-3 py-2 text-right font-medium">{usd(a.cost_usd)}</td>
                    <td className="px-3 py-2 text-right text-gray-400">{usd(a.task_cost_usd)}</td>
                    <td className="px-3 py-2 text-right text-emerald-300">{a.completed}</td>
                    <td className="px-3 py-2 text-right text-red-300">{a.failed || '·'}</td>
                    <td className="px-3 py-2 text-right text-amber-300">{a.rejected || '·'}</td>
                    <td className="px-3 py-2 text-right text-gray-500">{a.canceled || '·'}</td>
                    <td className="px-3 py-2 text-right text-gray-400">{a.open || '·'}</td>
                    <td className="px-3 py-2 text-right font-medium">{pct(a.completed, ended)}</td>
                    <td className="px-3 py-2 text-right">{a.completed ? usd(a.task_cost_usd / a.completed) : '—'}</td>
                    <td className="px-3 py-2 text-right font-medium">{a.scored ? a.mean_score.toFixed(1) : '—'}</td>
                    <td className="px-3 py-2 text-right">{a.verified ? `${a.passed}/${a.verified}` : '—'}</td>
                    <td className="px-3 py-2 text-right text-gray-400">{a.judged || '·'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </section>

      {(data?.projects?.length ?? 0) > 0 && (
        <section>
          <h3 className="mb-2 text-xs uppercase tracking-wider text-gray-500">Theo dự án</h3>
          <div className="overflow-x-auto rounded-lg ring-1 ring-gray-800">
            <table className="min-w-full text-sm tabular-nums">
              <thead className="bg-gray-900/80 text-[11px] uppercase tracking-wide text-gray-400">
                <tr>{['Dự án', 'Task', 'Xong', 'Chi phí', '$ / task xong', 'Điểm'].map(h => (
                  <th key={h} className="px-3 py-2 text-right first:text-left">{h}</th>
                ))}</tr>
              </thead>
              <tbody className="divide-y divide-gray-800">
                {data!.projects.map(p => (
                  <tr key={p.channel_id} className="text-gray-200">
                    <td className="px-3 py-2 text-left font-mono">{p.channel_id.replace(/^ch_/, '#')}</td>
                    <td className="px-3 py-2 text-right">{p.tasks}</td>
                    <td className="px-3 py-2 text-right">{p.completed}</td>
                    <td className="px-3 py-2 text-right font-medium">{usd(p.cost_usd)}</td>
                    <td className="px-3 py-2 text-right">{p.completed ? usd(p.cost_usd / p.completed) : '—'}</td>
                    <td className="px-3 py-2 text-right">{p.mean_score ? p.mean_score.toFixed(1) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}

      <div className="text-xs text-gray-500 space-y-1 max-w-3xl">
        <p><b className="text-gray-400">Chi phí</b> là con số CLI tự báo theo giá API niêm yết, kể cả khi gói đang dùng là thuê bao: dùng để so giữa các agent, không phải hoá đơn.</p>
        <p><b className="text-gray-400">Hoàn thành</b> = xong / (xong + hỏng + bị trả). Task bị huỷ là quyết định của owner nên không tính là trượt.</p>
        <p><b className="text-gray-400">Điểm</b> (1–10) và <b className="text-gray-400">Qua</b> là giám khảo đọc goal đã xong theo tiêu chí nghiệm thu. Giám khảo không bao giờ chấm việc của chính nó: khi nó là người làm, một peer khác chấm thay.</p>
      </div>
    </div>
  )
}

function Tile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-lg bg-gray-900 ring-1 ring-gray-800 px-4 py-3">
      <div className="text-[11px] uppercase tracking-wider text-gray-500">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums text-gray-100">{value}</div>
      {hint && <div className="mt-0.5 text-xs text-gray-500">{hint}</div>}
    </div>
  )
}
