import { useCallback, useEffect, useMemo, useState } from 'react'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// Which token carried the work, over time — one row per token on one shared
// scale, each bar the turns in an hour (24h) or a day (7/30 days), stacked by
// the agent that ran them. The live counters on the cards reset with every
// restart; this reads the turns kept for 30 days in the shared state, so a
// token used all afternoon no longer shows "0 lượt" in the evening.

interface Usage {
  provider: string
  bucket_seconds: number
  buckets: number[]
  agents: string[]
  tokens: { name: string; kind: string; total: number; by_agent: Record<string, number[]> }[]
}

// Agent identity colors — the same hues as their badges everywhere, one step
// darker so bars hold contrast on both surfaces (validated with the dataviz
// palette script: lightness band, CVD separation, 3:1 against the surface).
const AGENT_COLOR: Record<string, { dark: string; light: string }> = {
  bomclaw: { dark: '#0084d1', light: '#0084d1' },
  bomclaw2: { dark: '#009966', light: '#009966' },
  bomclaw3: { dark: '#8e51ff', light: '#7f22fe' },
}
const FALLBACK = { dark: '#e17100', light: '#e17100' }

const RANGES = [
  { days: 1, label: '24 giờ' },
  { days: 7, label: '7 ngày' },
  { days: 30, label: '30 ngày' },
]

export default function TokenUsage({ call, provider, lastUsed }: {
  call: Call; provider: string; lastUsed: Record<string, string | undefined>
}) {
  const [days, setDays] = useState(1)
  const [u, setU] = useState<Usage | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [table, setTable] = useState(false)
  const [hover, setHover] = useState<{ token: string; i: number; x: number; y: number } | null>(null)
  const light = typeof document !== 'undefined' && document.documentElement.classList.contains('light')
  const color = (a: string) => (AGENT_COLOR[a] ?? FALLBACK)[light ? 'light' : 'dark']

  const load = useCallback(async () => {
    try {
      setU(await call('tokens.usage', { provider, days }))
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call, provider, days])
  useEffect(() => {
    void load()
    const t = setInterval(load, 30000)
    return () => clearInterval(t)
  }, [load])

  const rows = useMemo(() => {
    if (!u) return []
    return [...u.tokens].sort((a, b) => b.total - a.total || a.name.localeCompare(b.name))
  }, [u])
  // One scale for every row, so two tokens' bars compare.
  const max = useMemo(() => {
    let m = 1
    for (const t of rows) {
      for (let i = 0; i < (u?.buckets.length ?? 0); i++) {
        let s = 0
        for (const a of Object.keys(t.by_agent)) s += t.by_agent[a][i] ?? 0
        if (s > m) m = s
      }
    }
    return m
  }, [rows, u])

  const label = (ts: number, full = false) => {
    const d = new Date(ts * 1000)
    if (u && u.bucket_seconds < 86400) return d.toLocaleTimeString('vi-VN', { hour: '2-digit', minute: '2-digit' }) + (full ? ' ' + d.toLocaleDateString('vi-VN', { day: '2-digit', month: '2-digit' }) : '')
    return d.toLocaleDateString('vi-VN', { day: '2-digit', month: '2-digit' })
  }

  const n = u?.buckets.length ?? 0
  const W = 600, H = 34, GAP = 2
  const bw = n ? (W - GAP * (n - 1)) / n : 0

  return (
    <div className="mt-3 rounded-lg ring-1 ring-gray-800 bg-gray-950/40 p-3">
      {/* Filters in one row, above the chart. */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-gray-200 font-medium">Lượt dùng theo thời gian</span>
        <div role="tablist" className="flex rounded-full ring-1 ring-gray-800 p-0.5">
          {RANGES.map(r => (
            <button key={r.days} role="tab" aria-selected={days === r.days} onClick={() => setDays(r.days)}
              className={`px-2 h-6 rounded-full text-[11px] cursor-pointer transition-colors ${days === r.days ? 'bg-gray-800 text-gray-100' : 'text-gray-500 hover:text-gray-200'}`}>
              {r.label}
            </button>
          ))}
        </div>
        {/* Legend: always, so identity is never color alone. */}
        <div className="flex items-center gap-3 text-[11px] text-gray-400">
          {(u?.agents ?? []).map(a => (
            <span key={a} className="inline-flex items-center gap-1">
              <span className="h-2.5 w-2.5 rounded-sm" style={{ background: color(a) }} aria-hidden />{a}
            </span>
          ))}
        </div>
        <button onClick={() => setTable(t => !t)} className="ml-auto text-[11px] text-gray-500 hover:text-gray-200 cursor-pointer">
          {table ? 'biểu đồ' : 'bảng'}
        </button>
      </div>
      {err && <div className="mt-2 text-xs text-red-300">{err}</div>}
      {u && rows.length === 0 && <div className="mt-2 text-xs text-gray-500">Chưa có token nào.</div>}

      {u && table ? (
        <table className="mt-2 w-full text-[11px]">
          <thead>
            <tr className="text-gray-500 text-left">
              <th className="font-normal py-1">token</th>
              {u.agents.map(a => <th key={a} className="font-normal text-right">{a}</th>)}
              <th className="font-normal text-right">tổng</th>
              <th className="font-normal text-right">dùng gần nhất</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(t => (
              <tr key={t.name} className="border-t border-gray-800 text-gray-300">
                <td className="py-1 font-mono">{t.name}</td>
                {u.agents.map(a => <td key={a} className="text-right tabular-nums">{(t.by_agent[a] ?? []).reduce((x, y) => x + y, 0) || '·'}</td>)}
                <td className="text-right tabular-nums text-gray-100">{t.total}</td>
                <td className="text-right text-gray-500">{lastUsed[t.name] ? ago(lastUsed[t.name]!) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : u && (
        <div className="mt-2 space-y-1.5 relative" onMouseLeave={() => setHover(null)}>
          {rows.map((t, ri) => (
            <div key={t.name} className="grid grid-cols-[7.5rem_1fr] gap-2 items-end">
              <div className="min-w-0 pb-0.5">
                <div className="text-[12px] text-gray-200 font-mono truncate">{t.name}</div>
                <div className="text-[10.5px] text-gray-500 tabular-nums">
                  {t.total ? `${t.total} lượt` : 'không dùng'}{lastUsed[t.name] ? ` · ${ago(lastUsed[t.name]!)}` : ''}
                </div>
              </div>
              <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="w-full h-9" role="img"
                aria-label={`${t.name}: ${t.total} lượt trong ${RANGES.find(r => r.days === days)?.label}`}>
                {/* Baseline: recessive. */}
                <line x1={0} x2={W} y1={H - 0.5} y2={H - 0.5} stroke="currentColor" className="text-gray-800" strokeWidth={1} />
                {u.buckets.map((ts, i) => {
                  let y = H
                  const x = i * (bw + GAP)
                  const segs = u.agents.map(a => ({ a, v: t.by_agent[a]?.[i] ?? 0 })).filter(s => s.v > 0)
                  return (
                    <g key={ts}
                      onMouseEnter={e => setHover({ token: t.name, i, x: e.clientX, y: e.clientY })}
                      onMouseMove={e => setHover({ token: t.name, i, x: e.clientX, y: e.clientY })}>
                      {/* Hit target taller than the mark. */}
                      <rect x={x} y={0} width={bw + GAP} height={H} fill="transparent" />
                      {segs.map((s, si) => {
                        const h = Math.max(2, (s.v / max) * (H - 2))
                        y -= h
                        const top = si === segs.length - 1
                        return (
                          <rect key={s.a} x={x} y={y + (si ? 0 : 0)} width={Math.max(bw, 1)} height={Math.max(h - (si < segs.length - 1 ? 1 : 0), 1)}
                            rx={top ? Math.min(2, bw / 2) : 0} fill={color(s.a)}
                            opacity={hover && (hover.token !== t.name || hover.i !== i) ? 0.55 : 1} />
                        )
                      })}
                    </g>
                  )
                })}
              </svg>
              {ri === rows.length - 1 && (
                <div className="col-start-2 flex justify-between text-[10px] text-gray-600 tabular-nums -mt-1">
                  <span>{label(u.buckets[0])}</span>
                  <span>{label(u.buckets[Math.floor(n / 2)])}</span>
                  <span>{label(u.buckets[n - 1])}</span>
                </div>
              )}
            </div>
          ))}
          {hover && (() => {
            const t = rows.find(r => r.name === hover.token)
            if (!t) return null
            const parts = u.agents.map(a => ({ a, v: t.by_agent[a]?.[hover.i] ?? 0 })).filter(p => p.v > 0)
            const sum = parts.reduce((x, p) => x + p.v, 0)
            return (
              <div className="fixed z-50 pointer-events-none rounded-lg bg-gray-950 ring-1 ring-gray-700 shadow-xl px-2.5 py-1.5 text-[11px] text-gray-300"
                style={{ left: hover.x + 12, top: hover.y + 12 }}>
                <div className="text-gray-100 font-medium">{t.name} · {label(u.buckets[hover.i], true)}</div>
                {sum === 0 ? <div className="text-gray-500">không dùng</div> : parts.map(p => (
                  <div key={p.a} className="flex items-center gap-1.5">
                    <span className="h-2 w-2 rounded-sm" style={{ background: color(p.a) }} />{p.a}
                    <span className="ml-auto pl-3 tabular-nums text-gray-100">{p.v}</span>
                  </div>
                ))}
                {parts.length > 1 && <div className="border-t border-gray-800 mt-0.5 pt-0.5 flex">tổng<span className="ml-auto tabular-nums text-gray-100">{sum}</span></div>}
              </div>
            )
          })()}
        </div>
      )}
    </div>
  )
}
