import { useCallback, useEffect, useRef, useState } from 'react'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// The infrastructure a project room runs on, as the first thing its header
// says: which provider, whether it is connected, whose account and plan, which
// services, and whether PROD is live right now. Before this the header had a
// "DEV · PROD" button that said nothing until opened — a live site and a
// project that had never been deployed looked exactly the same.

export interface DBStatus { engine: string; name: string; exists: boolean; bytes: number; tables: number; error?: string }
export interface ZonePlan { zone: string; plan: string; price: number; currency?: string; frequency?: string }
export interface InfraAccount { name?: string; plan?: string; plan_hint?: string; zones?: ZonePlan[] }
export interface EnvStatus {
  channel_id: string
  project_dir?: string
  declared: boolean
  error?: string
  spec?: { static?: string; build?: string; domain?: string; name?: string; spa?: boolean; database?: { engine: string; file: string } }
  dev: {
    preview?: { declared: boolean; state?: string; url?: string; kind?: string }
    build?: { static_dir?: string; built: boolean; built_at?: string; files: number; database?: DBStatus }
  }
  prod: {
    provider?: string; title?: string
    declared: boolean; connected: boolean; error?: string; name?: string; deployed: boolean
    deployed_at?: string; url?: string; workers_dev?: string; domains?: string[]; want_domain?: string
    database?: DBStatus
    account?: InfraAccount
    discovered_from?: string
    services?: string[]
  }
  deploy?: { running: boolean; started: string; finished?: string; with_db: boolean; dry_run: boolean; error?: string; result?: { url?: string } }
}

// The last answer per room, so coming back to a room shows its infrastructure
// at once and refreshes underneath, instead of "checking…" every time.
const lastSeen = new Map<string, EnvStatus>()

/** useEnvStatus polls env.status: closely while a deploy runs, else each minute. */
export function useEnvStatus(call: Call, channelID: string, enabled = true) {
  const [st, setSt] = useState<EnvStatus | null>(() => lastSeen.get(channelID) ?? null)
  const [err, setErr] = useState<string | null>(null)
  const room = useRef(channelID)
  room.current = channelID
  const load = useCallback(async () => {
    if (!channelID || !enabled) return
    try {
      const s: EnvStatus = await call('env.status', { channel_id: channelID })
      lastSeen.set(channelID, s)
      // A slow answer for the room just left must not land on the next one.
      if (room.current !== channelID) return
      setSt(s)
      setErr(null)
    } catch (e: any) {
      if (room.current === channelID) setErr(String(e?.message ?? e))
    }
  }, [call, channelID, enabled])
  useEffect(() => { setSt(lastSeen.get(channelID) ?? null); setErr(null); void load() }, [load, channelID])
  useEffect(() => {
    if (!enabled) return
    const t = setInterval(load, st?.deploy?.running ? 2000 : 60000)
    return () => clearInterval(t)
  }, [load, enabled, st?.deploy?.running])
  return { st, err, reload: load }
}

export type Tone = 'live' | 'ok' | 'idle' | 'warn' | 'bad' | 'busy'

/** The tone of each tone: a dot, a ring, a text colour. Color is never the only
 *  signal — every state also has its own words. */
export const toneClass: Record<Tone, { dot: string; ring: string; text: string; soft: string }> = {
  live: { dot: 'bg-emerald-400', ring: 'ring-emerald-500/40', text: 'text-emerald-300', soft: 'bg-emerald-500/10' },
  ok: { dot: 'bg-sky-400', ring: 'ring-sky-500/35', text: 'text-sky-300', soft: 'bg-sky-500/10' },
  idle: { dot: 'bg-gray-500', ring: 'ring-gray-700', text: 'text-gray-400', soft: 'bg-gray-800/40' },
  warn: { dot: 'bg-amber-400', ring: 'ring-amber-500/40', text: 'text-amber-300', soft: 'bg-amber-500/10' },
  bad: { dot: 'bg-red-400', ring: 'ring-red-500/40', text: 'text-red-300', soft: 'bg-red-500/10' },
  busy: { dot: 'bg-orange-400', ring: 'ring-orange-500/40', text: 'text-orange-300', soft: 'bg-orange-500/10' },
}

/** prodState is PROD in one tone and a few words. */
export function prodState(st: EnvStatus | null): { tone: Tone; label: string; short: string } {
  if (!st) return { tone: 'idle', label: 'đang kiểm tra…', short: '…' }
  const p = st.prod
  if (st.deploy?.running) return { tone: 'busy', label: 'đang deploy…', short: 'deploy…' }
  if (!p.connected) return { tone: 'warn', label: 'chưa kết nối', short: 'chưa nối' }
  // Live but never declared in bomclaw.json: published by hand, found by name.
  if (!st.declared && p.deployed) return { tone: 'live', label: 'live', short: 'live' }
  if (!st.declared) return { tone: 'ok', label: 'đã kết nối · chưa có PROD', short: 'chưa PROD' }
  if (p.error) return { tone: 'bad', label: 'PROD lỗi', short: 'lỗi' }
  if (p.deployed) return { tone: 'live', label: 'live', short: 'live' }
  return { tone: 'ok', label: 'chưa deploy', short: 'chưa deploy' }
}

/** devState is the local Preview in one tone and a word. */
export function devState(st: EnvStatus | null): { tone: Tone; label: string } {
  const pv = st?.dev.preview
  if (!pv?.declared) return { tone: 'idle', label: 'chưa khai báo' }
  switch (pv.state) {
    case 'running': return { tone: 'live', label: 'đang chạy' }
    case 'starting': return { tone: 'busy', label: 'đang khởi động' }
    case 'exited': return { tone: 'bad', label: 'đã dừng (lỗi)' }
    default: return { tone: 'idle', label: 'đang tắt' }
  }
}

/** Dot is a status light; a live one breathes, unless motion is reduced. */
export function Dot({ tone, size = 'h-2 w-2' }: { tone: Tone; size?: string }) {
  const c = toneClass[tone]
  return (
    <span className={`relative inline-flex shrink-0 ${size}`} aria-hidden>
      {(tone === 'live' || tone === 'busy') && (
        <span className={`absolute inline-flex h-full w-full rounded-full ${c.dot} opacity-60 motion-safe:animate-ping`} />
      )}
      <span className={`relative inline-flex rounded-full ${size} ${c.dot}`} />
    </span>
  )
}

export function host(u?: string) {
  if (!u) return ''
  try { return new URL(u).host } catch { return u }
}

/**
 * InfraBadge is the room header's infrastructure pill: provider, PROD state and
 * where it answers, with DEV's light beside it. Hovering or focusing it shows
 * the account, plan and services; clicking opens the full environments panel.
 */
export function InfraBadge({ st, err, onOpen }: { st: EnvStatus | null; err: string | null; onOpen: () => void }) {
  const prod = prodState(st)
  const dev = devState(st)
  const p = st?.prod
  const c = toneClass[err ? 'bad' : prod.tone]
  const provider = p?.title || p?.provider || 'Cloudflare'
  const plan = p?.connected ? planLabel(st) : ''
  const where = prod.tone === 'live' ? host(p?.url) : ''
  const summary = err
    ? `Không đọc được trạng thái hạ tầng: ${err}`
    : `${provider}${plan ? ` (gói ${plan})` : ''}: ${prod.label}${where ? ` tại ${where}` : ''}. DEV: ${dev.label}. Bấm để mở Môi trường.`

  return (
    <span className="relative group shrink-0">
      <button
        onClick={e => { e.currentTarget.blur(); onOpen() }}
        aria-label={summary}
        className={`flex items-center gap-1.5 @2xl:gap-2 h-7 pl-2 pr-2.5 rounded-full ring-1 ${c.ring} ${c.soft} text-[11px] cursor-pointer
          transition-colors duration-200 hover:brightness-110 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400`}
      >
        <CloudIcon className={`h-3.5 w-3.5 shrink-0 ${c.text}`} />
        <span className="font-medium text-gray-200 hidden @2xl:inline">{provider}</span>
        {plan && (
          <span className={`hidden @xl:inline px-1.5 rounded-full text-[10px] font-medium ring-1 whitespace-nowrap ${
            /free/i.test(plan) ? 'bg-gray-900/60 text-gray-300 ring-gray-700' : 'bg-violet-500/15 text-violet-200 ring-violet-500/30'}`}>
            {plan}
          </span>
        )}
        <span className="h-3 w-px bg-gray-700 hidden @2xl:block" aria-hidden />
        <Dot tone={err ? 'bad' : prod.tone} />
        <span className={`${c.text} whitespace-nowrap`}>
          {err ? 'lỗi' : (
            <>
              <span className="@4xl:hidden">{prod.tone === 'live' ? 'live' : prod.short}</span>
              <span className="hidden @4xl:inline">{prod.tone === 'live' ? 'PROD live' : prod.label}</span>
            </>
          )}
        </span>
        {where && <span className="font-mono text-gray-400 max-w-[180px] truncate hidden @6xl:inline">{where}</span>}
        <span className="h-3 w-px bg-gray-700 hidden @3xl:block" aria-hidden />
        <span className="hidden @3xl:flex items-center gap-1 text-gray-400" title={`DEV: ${dev.label}`}>
          <Dot tone={dev.tone} size="h-1.5 w-1.5" />DEV
        </span>
      </button>

      {/* Details on hover or keyboard focus — never the only place a fact lives:
          the panel behind the click shows all of it. */}
      {st && !err && (
        <div
          role="tooltip"
          className="invisible opacity-0 translate-y-1 group-hover:visible group-hover:opacity-100 group-hover:translate-y-0
            group-focus-within:visible group-focus-within:opacity-100 group-focus-within:translate-y-0
            transition-all duration-150 motion-reduce:transition-none
            absolute left-0 top-full mt-2 z-40 w-96 max-w-[calc(100vw-2rem)] rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl p-3 space-y-2.5 text-xs"
        >
          <div className="flex items-center gap-2">
            <Dot tone={prod.tone} />
            <span className="text-gray-200 font-medium">{provider}</span>
            <span className={`ml-auto ${toneClass[prod.tone].text}`}>{prod.label}</span>
          </div>
          <Facts st={st} />
        </div>
      )}
    </span>
  )
}

/** Facts is the account / plan / services / address list, shared by the
 *  hover card and the environments panel. */
export function Facts({ st }: { st: EnvStatus }) {
  const p = st.prod
  const a = p.account
  const zones = projectZones(st)
  return (
    <dl className="grid grid-cols-[72px_1fr] gap-x-2 gap-y-1.5">
      <dt className="text-gray-500">Tài khoản</dt>
      <dd className="text-gray-200 min-w-0 truncate" title={a?.name}>
        {p.connected ? (a?.name || 'đã kết nối') : <span className="text-amber-300">chưa kết nối</span>}
      </dd>
      <dt className="text-gray-500">Gói</dt>
      <dd className="min-w-0 space-y-1">
        {zones.length === 0 && !a?.plan && <span className="text-gray-500">{p.connected ? 'chưa đọc được' : '—'}</span>}
        {zones.map(z => (
          <div key={z.zone} className="flex items-center gap-1.5 min-w-0">
            <PlanChip name={z.plan} />
            <span className="font-mono text-gray-400 truncate">{z.zone}</span>
            {z.price > 0 && <span className="text-gray-500 whitespace-nowrap">{z.price} {z.currency}{z.frequency ? `/${z.frequency}` : ''}</span>}
          </div>
        ))}
        <div className="flex items-center gap-1.5 min-w-0">
          <span className="text-gray-500">Workers</span>
          {a?.plan
            ? <PlanChip name={a.plan} />
            : <span className="text-gray-500 truncate" title={a?.plan_hint}>
                chưa đọc được
                {p.connected && a?.plan_hint && p.provider !== 'fly' && (
                  <a href="/admin/connections" title={a.plan_hint} className="ml-1 text-sky-300 hover:underline whitespace-nowrap">cấp quyền Billing ▸</a>
                )}
              </span>}
        </div>
      </dd>
      {!!p.services?.length && (
        <>
          <dt className="text-gray-500">Service</dt>
          <dd className="flex flex-wrap gap-1">
            {p.services.map(s => (
              <span key={s} className="px-1.5 rounded bg-gray-800/80 ring-1 ring-gray-700 text-gray-300 text-[10.5px]">{s}</span>
            ))}
          </dd>
        </>
      )}
      {p.url && (
        <>
          <dt className="text-gray-500">Địa chỉ</dt>
          <dd className="min-w-0">
            <a href={p.url} target="_blank" rel="noreferrer" className="font-mono text-sky-300 hover:underline break-all">{host(p.url)}</a>
          </dd>
        </>
      )}
      {p.deployed_at && (
        <>
          <dt className="text-gray-500">Deploy</dt>
          <dd className="text-gray-300">{ago(p.deployed_at)}</dd>
        </>
      )}
    </dl>
  )
}

export function CloudIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" className={className} aria-hidden>
      <path d="M6 15.5h8.5a3.5 3.5 0 0 0 .6-6.95A5 5 0 0 0 5.5 9.1 3.2 3.2 0 0 0 6 15.5Z" strokeLinejoin="round" />
    </svg>
  )
}

export function CodeIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" className={className} aria-hidden>
      <path d="m7 6-4 4 4 4M13 6l4 4-4 4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

export function BookIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" className={className} aria-hidden>
      <path d="M4 4.5A1.5 1.5 0 0 1 5.5 3H16v12H5.5A1.5 1.5 0 0 0 4 16.5v-12ZM4 16.5A1.5 1.5 0 0 0 5.5 18H16" strokeLinejoin="round" />
    </svg>
  )
}

export function SignalIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" className={className} aria-hidden>
      <path d="M10 11v6M6.5 7.5a5 5 0 0 1 7 0M4 5a8.5 8.5 0 0 1 12 0" strokeLinecap="round" />
      <circle cx="10" cy="10.5" r="1" fill="currentColor" />
    </svg>
  )
}

/** projectZones is the zone the project's domain lives on, or every zone of
 *  the account when the project has no domain of its own. */
export function projectZones(st: EnvStatus | null): ZonePlan[] {
  const all = st?.prod.account?.zones ?? []
  const domain = st?.prod.domains?.[0] || st?.prod.want_domain || st?.spec?.domain || ''
  const own = domain ? all.filter(z => domain === z.zone || domain.endsWith('.' + z.zone)) : []
  return own.length ? own : all
}

/** planLabel is the plan in a word or two, for the header pill: the Workers
 *  plan when it is known, else the plan of the project's domain. */
export function planLabel(st: EnvStatus | null): string {
  const a = st?.prod.account
  if (a?.plan) return a.plan
  const z = projectZones(st)
  if (!z.length) return ''
  const names = [...new Set(z.map(x => x.plan.replace(/ Website$/, '')))]
  return names.join(' / ')
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
