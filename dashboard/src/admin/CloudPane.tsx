import { useCallback, useEffect, useMemo, useState } from 'react'
import { ago } from './format'
import { CloudIcon, Dot, host, shortVer, toneClass, type InfraAccount, type ProdVersion, type GitVersion, type Tone } from './InfraBadge'

type Call = (method: string, params?: any) => Promise<any>

// Admin → Infra → Cloud: the providers every project's PROD runs on —
// Cloudflare, Fly.io, AWS, Vercel. Per
// provider: connected or not, whose account, which plan, what the account did
// in the last day (requests, errors, CPU, databases), and which projects live
// there. A project room shows its own environments and versions; the account
// and its traffic are shared by every project on it, so they are here, once.

interface MetricPoint { at: string; requests: number; errors: number }
interface WorkerMetric { name: string; kind?: string; project?: string; requests: number; errors: number; subrequests: number; cpu_p50_us: number; cpu_p99_us: number }
interface Cost { amount: number; currency: string; period: string; by_service?: { name: string; amount: number }[] }
interface DBMetric { id: string; name: string; bytes: number; read_queries: number; write_queries: number; rows_read: number; rows_written: number }
interface Metrics {
  since: string; until: string
  requests: number; errors: number; subrequests: number
  hourly?: MetricPoint[]; workers?: WorkerMetric[]; databases?: DBMetric[]
  scripts: number; error?: string
  unit?: string; deploys?: number; deploy_errors?: number; cost?: Cost; note?: string
}
interface CloudProject {
  channel_id: string; name: string; declared: boolean; dev_ahead: number; running: boolean; error?: string
  git?: GitVersion
  prod: {
    provider?: string; connected: boolean; deployed: boolean; deployed_at?: string; url?: string; name?: string
    error?: string; version?: ProdVersion; discovered_from?: string; services?: string[]
  }
}
interface CloudProvider {
  name: string; title: string; built: boolean; connected: boolean; hint?: string
  account?: InfraAccount; metrics?: Metrics; projects: CloudProject[]
}
interface CloudStatus { at: string; window: string; providers: CloudProvider[] }

const num = (n: number) => n.toLocaleString('vi-VN')
const kb = (b: number) => b >= 2 ** 20 ? `${(b / 2 ** 20).toFixed(1)} MB` : `${Math.max(1, Math.round(b / 1024))} KB`
const ms = (us: number) => us >= 1000 ? `${(us / 1000).toFixed(1)} ms` : `${Math.round(us)} µs`

function Btn({ onClick, children, disabled }: { onClick: () => void; children: React.ReactNode; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled}
      className="px-1.5 py-0.5 text-[11px] rounded ring-1 ring-gray-800 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40 disabled:opacity-40">
      {children}
    </button>
  )
}

export default function CloudPane({ call }: { call: Call }) {
  const [st, setSt] = useState<CloudStatus | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async (fresh = false) => {
    setBusy(true)
    try {
      setSt(await call('cloud.status', { fresh }))
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }, [call])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    const t = setInterval(() => load(), 60000)
    return () => clearInterval(t)
  }, [load])

  if (!st) {
    return <div className="text-sm text-gray-500">{err ? <span className="text-red-300">{err}</span> : 'Đang hỏi các nhà cung cấp…'}</div>
  }
  const built = st.providers.filter(p => p.built)
  const planned = st.providers.filter(p => !p.built)

  return (
    <div className="space-y-4">
      <div className="flex items-baseline gap-3 flex-wrap">
        <span className="text-xs text-gray-500">hạ tầng PROD của mọi dự án — tài khoản, gói, metric 24 giờ qua, dự án đang chạy trên đó</span>
        <div className="ml-auto flex items-center gap-2 text-[11px] text-gray-500">
          <span>{new Date(st.at).toLocaleTimeString()}</span>
          <Btn onClick={() => load(true)} disabled={busy}>{busy ? 'đang hỏi…' : 'làm mới'}</Btn>
        </div>
      </div>
      {err && <div className="text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2 whitespace-pre-wrap">{err}</div>}

      {built.map(p => <ProviderCard key={p.name} p={p} />)}

      {planned.length > 0 && (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {planned.map(p => (
            <div key={p.name} className="rounded-lg ring-1 ring-gray-800 bg-gray-900/20 p-3 flex items-start gap-3">
              <span className="h-8 w-8 shrink-0 grid place-items-center rounded-lg ring-1 ring-gray-800 text-gray-600"><CloudIcon className="h-4 w-4" /></span>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm text-gray-300">{p.title}</span>
                  <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-500">chưa hỗ trợ</span>
                </div>
                <div className="text-[11px] text-gray-500 mt-0.5">{p.hint}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function providerTone(p: CloudProvider): { tone: Tone; label: string } {
  if (!p.connected) return { tone: 'warn', label: 'chưa kết nối' }
  const bad = p.projects.filter(x => x.prod.error).length
  if (bad) return { tone: 'bad', label: `${bad} dự án lỗi` }
  const m = p.metrics
  if (m && m.requests > 0 && m.errors / m.requests > 0.05) return { tone: 'warn', label: 'tỉ lệ lỗi cao' }
  return { tone: 'live', label: 'hoạt động bình thường' }
}

function ProviderCard({ p }: { p: CloudProvider }) {
  const t = providerTone(p)
  const c = toneClass[t.tone]
  const a = p.account
  const m = p.metrics
  const live = p.projects.filter(x => x.prod.deployed).length
  // Worker name → project room, so a busy Worker names the project it is.
  const byWorker = useMemo(() => new Map(p.projects.filter(x => x.prod.name).map(x => [x.prod.name!, x])), [p.projects])

  return (
    <section className={`rounded-xl ring-1 ${c.ring} bg-gray-900/40 overflow-hidden`}>
      <header className="flex items-center gap-3 px-4 py-3 border-b border-gray-800 flex-wrap">
        <span className={`h-9 w-9 shrink-0 grid place-items-center rounded-xl bg-gray-900 ring-1 ${c.ring}`}>
          <CloudIcon className={`h-5 w-5 ${c.text}`} />
        </span>
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-base font-semibold text-gray-100">{p.title}</span>
            <span className={`inline-flex items-center gap-1.5 h-5 px-2 rounded-full text-[11px] ${c.text} ring-1 ${c.ring}`}>
              <Dot tone={t.tone} size="h-1.5 w-1.5" />{t.label}
            </span>
          </div>
          <div className="text-[11px] text-gray-500 mt-0.5">
            {p.connected ? <>tài khoản <span className="text-gray-300">{a?.name || '—'}</span> · {live}/{p.projects.length} dự án live</> : p.hint}
          </div>
        </div>
        {!p.connected && (
          <a href="/admin/connections" className="ml-auto text-xs px-2.5 py-1 rounded-lg ring-1 ring-amber-500/40 bg-amber-500/15 text-amber-200 hover:bg-amber-500/25">
            Kết nối {p.title} ▸
          </a>
        )}
      </header>

      {p.connected && (
        <div className="p-4 space-y-4">
          {/* Account & plan */}
          <dl className="grid grid-cols-[88px_1fr] gap-x-3 gap-y-1.5 text-xs">
            <dt className="text-gray-500">Gói</dt>
            <dd className="flex flex-wrap items-center gap-1.5 min-w-0">
              {p.name === 'cloudflare' && (
                <>
                  <span className="text-gray-500">Workers</span>
                  {a?.plan ? <PlanChip name={a.plan} /> : (
                    <span className="text-gray-500" title={a?.plan_hint}>
                      chưa đọc được{a?.plan_hint && <a href="/admin/connections" className="ml-1 text-sky-300 hover:underline">cấp quyền Billing ▸</a>}
                    </span>
                  )}
                </>
              )}
              {p.name !== 'cloudflare' && (a?.plan ? <PlanChip name={a.plan} /> : <span className="text-gray-500">{a?.plan_hint || '—'}</span>)}
            </dd>
            {!!a?.zones?.length && (
              <>
                <dt className="text-gray-500">Domain</dt>
                <dd className="flex flex-wrap gap-x-3 gap-y-1 min-w-0">
                  {a.zones.map(z => (
                    <span key={z.zone} className="inline-flex items-center gap-1.5">
                      <span className="font-mono text-gray-300">{z.zone}</span><PlanChip name={z.plan} />
                      {z.price > 0 && <span className="text-gray-500">{z.price} {z.currency}{z.frequency ? `/${z.frequency}` : ''}</span>}
                    </span>
                  ))}
                </dd>
              </>
            )}
          </dl>

          {m && <MetricsBlock m={m} byWorker={byWorker} provider={p.name} />}
          {!m && <div className="text-xs text-gray-500">{p.title} chưa có metric qua API ở đây.</div>}

          <Projects projects={p.projects} />
        </div>
      )}
    </section>
  )
}

function Tile({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: Tone }) {
  return (
    <div className="rounded-lg ring-1 ring-gray-800 bg-gray-950/40 p-3">
      <div className="text-[10px] uppercase tracking-wider text-gray-500">{label}</div>
      <div className="mt-1 text-lg text-gray-100 font-mono">{value}</div>
      {sub && <div className={`mt-0.5 text-[11px] ${tone ? toneClass[tone].text : 'text-gray-500'}`}>{sub}</div>}
    </div>
  )
}

const money = (n: number, cur: string) => `${cur === 'USD' ? '$' : ''}${n.toFixed(2)}${cur === 'USD' ? '' : ' ' + cur}`

function MetricsBlock({ m, byWorker, provider }: { m: Metrics; byWorker: Map<string, CloudProject>; provider: string }) {
  const rate = m.requests ? (m.errors / m.requests) * 100 : 0
  const dbBytes = (m.databases ?? []).reduce((s, d) => s + d.bytes, 0)
  const unit = m.unit || 'Worker'
  const deploysOnly = provider === 'vercel'
  const cpu = (m.workers ?? []).some(w => w.cpu_p50_us > 0)
  const tiles: React.ReactNode[] = deploysOnly ? [
    <Tile key="d" label="Deploy · 24h" value={num(m.deploys ?? 0)} sub={`${(m.workers ?? []).length} project có deploy`} />,
    <Tile key="e" label="Deploy lỗi · 24h" value={num(m.deploy_errors ?? 0)} sub={m.deploys ? `${(((m.deploy_errors ?? 0) / m.deploys) * 100).toFixed(0)}% deploy` : 'không có deploy'}
      tone={(m.deploy_errors ?? 0) > 0 ? 'warn' : undefined} />,
    <Tile key="p" label="Projects" value={String(m.scripts)} />,
  ] : [
    <Tile key="r" label="Requests · 24h" value={num(m.requests)} sub={m.subrequests ? `${num(m.subrequests)} subrequest` : undefined} />,
    <Tile key="e" label={provider === 'cloudflare' ? 'Lỗi · 24h' : 'Lỗi 5xx · 24h'} value={num(m.errors)} sub={m.requests ? `${rate.toFixed(2)}% request` : 'không có request'}
      tone={rate > 5 ? 'bad' : rate > 1 ? 'warn' : undefined} />,
    <Tile key="w" label={provider === 'cloudflare' ? 'Workers' : 'Resource'} value={String(m.scripts)} sub={`${(m.workers ?? []).filter(w => w.requests > 0).length} có traffic`} />,
  ]
  if (m.databases) tiles.push(<Tile key="db" label="D1" value={String(m.databases.length)} sub={dbBytes ? `tổng ${kb(dbBytes)}` : 'chưa có database'} />)
  if (m.cost) tiles.push(<Tile key="c" label="Chi phí tháng này" value={money(m.cost.amount, m.cost.currency)} sub={m.cost.period} />)
  return (
    <div className="space-y-3">
      {m.error && <div className="text-xs text-amber-300 rounded-lg bg-amber-500/10 ring-1 ring-amber-500/30 px-2.5 py-1.5">⚠ {m.error}</div>}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">{tiles}</div>
      {m.note && <div className="text-[11px] text-gray-500">ⓘ {m.note}</div>}
      {!!m.cost?.by_service?.length && (
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-gray-400">
          <span className="text-gray-500">Chi phí theo dịch vụ:</span>
          {m.cost.by_service.slice(0, 8).map(c => (
            <span key={c.name}>{c.name} <span className="font-mono text-gray-200">{money(c.amount, m.cost!.currency)}</span></span>
          ))}
        </div>
      )}

      {!!m.hourly?.length && m.requests > 0 && <HourlyBars points={m.hourly} />}

      {!!m.workers?.length && (
        <div className="rounded-lg ring-1 ring-gray-800 overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-[10px] uppercase tracking-wider text-gray-500">
              <tr className="border-b border-gray-800">
                <th className="text-left font-normal px-3 py-1.5">{unit}</th>
                <th className="text-right font-normal px-2 py-1.5">{deploysOnly ? 'Deploy' : 'Requests'}</th>
                <th className="text-right font-normal px-2 py-1.5">Lỗi</th>
                {cpu && <th className="text-right font-normal px-2 py-1.5">CPU p50</th>}
                {cpu && <th className="text-right font-normal px-3 py-1.5">CPU p99</th>}
              </tr>
            </thead>
            <tbody>
              {m.workers.map(w => {
                const proj = byWorker.get(w.project || w.name)
                return (
                  <tr key={(w.kind ?? '') + w.name} className="border-t border-gray-800/50">
                    <td className="px-3 py-1.5 font-mono text-gray-200">
                      {w.kind && w.kind !== 'deployments' && <span className="mr-1.5 font-sans text-[10px] px-1 rounded ring-1 ring-gray-700 text-gray-400">{w.kind}</span>}
                      {w.name}
                      {w.project && w.project !== w.name && !proj && <span className="ml-2 font-sans text-[11px] text-gray-500">{w.project}</span>}
                      {proj && <a href={`/admin/messages/${proj.channel_id}`} className="ml-2 font-sans text-[11px] text-sky-300 hover:underline">#{proj.name}</a>}
                    </td>
                    <td className="px-2 py-1.5 text-right font-mono text-gray-300">{num(w.requests)}</td>
                    <td className={`px-2 py-1.5 text-right font-mono ${w.errors ? 'text-red-300' : 'text-gray-500'}`}>{num(w.errors)}</td>
                    {cpu && <td className="px-2 py-1.5 text-right font-mono text-gray-400">{ms(w.cpu_p50_us)}</td>}
                    {cpu && <td className="px-3 py-1.5 text-right font-mono text-gray-400">{ms(w.cpu_p99_us)}</td>}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {!!m.databases?.length && (
        <div className="rounded-lg ring-1 ring-gray-800 overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-[10px] uppercase tracking-wider text-gray-500">
              <tr className="border-b border-gray-800">
                <th className="text-left font-normal px-3 py-1.5">D1 database</th>
                <th className="text-right font-normal px-2 py-1.5">Dung lượng</th>
                <th className="text-right font-normal px-2 py-1.5">Query đọc</th>
                <th className="text-right font-normal px-2 py-1.5">Query ghi</th>
                <th className="text-right font-normal px-3 py-1.5">Dòng đọc / ghi</th>
              </tr>
            </thead>
            <tbody>
              {m.databases.map(d => (
                <tr key={d.id} className="border-t border-gray-800/50">
                  <td className="px-3 py-1.5 font-mono text-gray-200">{d.name}</td>
                  <td className="px-2 py-1.5 text-right font-mono text-gray-300">{kb(d.bytes)}</td>
                  <td className="px-2 py-1.5 text-right font-mono text-gray-400">{num(d.read_queries)}</td>
                  <td className="px-2 py-1.5 text-right font-mono text-gray-400">{num(d.write_queries)}</td>
                  <td className="px-3 py-1.5 text-right font-mono text-gray-500">{num(d.rows_read)} / {num(d.rows_written)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

/** HourlyBars is requests per hour over the window: one series, so no legend —
 *  the title names it. Hours with errors carry a red cap and say so on hover. */
function HourlyBars({ points }: { points: MetricPoint[] }) {
  const [hover, setHover] = useState<number | null>(null)
  const max = Math.max(1, ...points.map(p => p.requests))
  const W = 100 / points.length
  const h = hover != null ? points[hover] : null
  return (
    <div className="rounded-lg ring-1 ring-gray-800 bg-gray-950/40 p-3">
      <div className="flex items-baseline gap-2 text-[11px]">
        <span className="text-gray-300">Requests theo giờ</span>
        <span className="text-gray-500">đỉnh {num(max)}/giờ</span>
        <span className="ml-auto text-gray-400 font-mono min-h-[1em]">
          {h ? <>{new Date(h.at).toLocaleString([], { hour: '2-digit', minute: '2-digit', day: '2-digit', month: '2-digit' })} · {num(h.requests)} request{h.errors ? <span className="text-red-300"> · {num(h.errors)} lỗi</span> : null}</> : null}
        </span>
      </div>
      <svg viewBox="0 0 100 40" preserveAspectRatio="none" className="mt-2 w-full h-20" role="img"
        aria-label={`Requests theo giờ trong 24 giờ qua, đỉnh ${max} mỗi giờ`} onMouseLeave={() => setHover(null)}>
        <line x1="0" x2="100" y1="39.8" y2="39.8" stroke="currentColor" strokeWidth="0.4" className="text-gray-800" vectorEffect="non-scaling-stroke" />
        {points.map((p, i) => {
          const bh = p.requests ? Math.max(1.2, (p.requests / max) * 38) : 0
          const eh = p.errors ? Math.max(1, (p.errors / max) * 38) : 0
          const x = i * W + W * 0.12
          const w = W * 0.76
          return (
            <g key={p.at} onMouseEnter={() => setHover(i)}>
              <rect x={i * W} y="0" width={W} height="40" fill="transparent" />
              {bh > 0 && <rect x={x} y={40 - bh} width={w} height={bh} rx="0.6"
                className={hover === i ? 'fill-sky-300' : 'fill-sky-500/70'} />}
              {eh > 0 && <rect x={x} y={40 - bh} width={w} height={Math.min(eh, bh)} className="fill-red-400" />}
            </g>
          )
        })}
      </svg>
      <div className="flex justify-between text-[10px] text-gray-600 font-mono mt-1">
        <span>{new Date(points[0].at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
        <span>bây giờ</span>
      </div>
    </div>
  )
}

function Projects({ projects }: { projects: CloudProject[] }) {
  if (!projects.length) return <div className="text-xs text-gray-500">Chưa dự án nào deploy lên đây.</div>
  return (
    <div className="rounded-lg ring-1 ring-gray-800 overflow-x-auto">
      <table className="w-full text-xs">
        <thead className="text-[10px] uppercase tracking-wider text-gray-500">
          <tr className="border-b border-gray-800">
            <th className="text-left font-normal px-3 py-1.5">Dự án</th>
            <th className="text-left font-normal px-2 py-1.5">PROD</th>
            <th className="text-left font-normal px-2 py-1.5">Phiên bản</th>
            <th className="text-left font-normal px-2 py-1.5">Publish</th>
            <th className="text-left font-normal px-3 py-1.5">Địa chỉ</th>
          </tr>
        </thead>
        <tbody>
          {projects.map(x => {
            const p = x.prod
            const tone: Tone = x.running ? 'busy' : p.error ? 'bad' : p.deployed ? (x.dev_ahead > 0 ? 'warn' : 'live') : 'idle'
            const label = x.running ? 'đang deploy' : p.error ? 'lỗi' : p.deployed ? 'live' : 'chưa deploy'
            const ver = p.version?.commit || p.version?.id
            return (
              <tr key={x.channel_id} className="border-t border-gray-800/50">
                <td className="px-3 py-1.5">
                  <a href={`/admin/messages/${x.channel_id}`} className="text-gray-200 hover:text-sky-300">#{x.name}</a>
                  {!x.declared && <span className="ml-1.5 text-[10px] text-gray-500" title={p.discovered_from}>ngoài dashboard</span>}
                </td>
                <td className="px-2 py-1.5">
                  <span className={`inline-flex items-center gap-1.5 ${toneClass[tone].text}`} title={p.error}><Dot tone={tone} size="h-1.5 w-1.5" />{label}</span>
                </td>
                <td className="px-2 py-1.5 font-mono text-gray-300 whitespace-nowrap">
                  {ver ? shortVer(ver) : <span className="text-gray-600">—</span>}
                  {x.dev_ahead > 0 && <span className="ml-1.5 font-sans text-[11px] text-amber-300">DEV +{x.dev_ahead}</span>}
                </td>
                <td className="px-2 py-1.5 text-gray-400 whitespace-nowrap">{p.deployed_at ? ago(p.deployed_at) : '—'}</td>
                <td className="px-3 py-1.5 font-mono">
                  {p.url ? <a href={p.url} target="_blank" rel="noreferrer" className="text-sky-300 hover:underline">{host(p.url)}</a> : <span className="text-gray-600">—</span>}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function PlanChip({ name }: { name: string }) {
  const free = /free/i.test(name)
  return (
    <span className={`inline-flex items-center px-1.5 rounded ring-1 whitespace-nowrap ${
      free ? 'bg-gray-800/80 text-gray-200 ring-gray-700' : 'bg-violet-500/15 text-violet-200 ring-violet-500/30'}`}>
      {name}
    </span>
  )
}
