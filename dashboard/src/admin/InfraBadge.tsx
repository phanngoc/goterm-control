import { useCallback, useEffect, useRef, useState } from 'react'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// A project room's environments, as the first thing its header says: is PROD
// live, which version it runs, and whether DEV has moved on since. The
// provider behind PROD — its account, plan, traffic — is Admin → Infra's to
// show (CloudPane), once for every project on it, not repeated in each room.

export interface DBStatus { engine: string; name: string; exists: boolean; bytes: number; tables: number; error?: string }
export interface ZonePlan { zone: string; plan: string; price: number; currency?: string; frequency?: string }
export interface InfraAccount { name?: string; plan?: string; plan_hint?: string; zones?: ZonePlan[] }
export interface GitVersion { commit: string; short: string; branch?: string; subject?: string; at?: string; dirty: number }
export interface ProdVersion { id?: string; message?: string; commit?: string; by?: string }
export interface EnvStatus {
  channel_id: string
  project_dir?: string
  declared: boolean
  error?: string
  spec?: { static?: string; build?: string; domain?: string; name?: string; spa?: boolean; database?: { engine: string; file: string } }
  dev: {
    preview?: { declared: boolean; state?: string; url?: string; kind?: string }
    build?: { static_dir?: string; built: boolean; built_at?: string; files: number; database?: DBStatus }
    git?: GitVersion
  }
  prod: {
    provider?: string; title?: string
    declared: boolean; connected: boolean; error?: string; name?: string; deployed: boolean
    version?: ProdVersion
    deployed_at?: string; url?: string; workers_dev?: string; domains?: string[]; want_domain?: string
    database?: DBStatus
    account?: InfraAccount
    discovered_from?: string
    services?: string[]
  }
  deploy?: { running: boolean; started: string; finished?: string; with_db: boolean; dry_run: boolean; error?: string; result?: { url?: string } }
  /** Commits DEV has that PROD does not run; -1 when it cannot be told. */
  dev_ahead: number
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

/** shortVer is a version id or commit in a few characters. */
export function shortVer(v?: string) { return v ? v.slice(0, 7) : '' }

/** prodVersion is PROD's version in a word: the commit it was built from when
 *  the deploy recorded one, else the provider's own version id. */
export function prodVersion(st: EnvStatus | null): { text: string; commit: boolean } | null {
  const v = st?.prod.version
  if (!v) return null
  if (v.commit) return { text: shortVer(v.commit), commit: true }
  if (v.id) return { text: shortVer(v.id), commit: false }
  return null
}

/** devVersion is DEV's checkout in a word. */
export function devVersion(st: EnvStatus | null): string {
  const g = st?.dev.git
  if (!g) return ''
  return g.short + (g.dirty ? '*' : '')
}

/**
 * projectState is the project in one tone and a sentence: is it live, is
 * what is live the version being worked on, did the last publish go out.
 */
export function projectState(st: EnvStatus | null): { tone: Tone; label: string } {
  if (!st) return { tone: 'idle', label: 'đang kiểm tra…' }
  const p = st.prod
  if (st.deploy?.running) return { tone: 'busy', label: 'Đang publish bản mới…' }
  if (st.error) return { tone: 'bad', label: st.error }
  if (!st.declared && p.deployed) return { tone: 'live', label: 'Đang live — publish ngoài dashboard, chưa thiết lập ở đây' }
  if (!st.declared) return { tone: 'idle', label: 'Chưa có PROD — dự án mới chạy ở DEV' }
  if (!p.connected) return { tone: 'warn', label: `Chưa kết nối ${p.title || p.provider || 'nhà cung cấp'} — không publish được` }
  if (p.error) return { tone: 'bad', label: 'PROD lỗi: ' + p.error }
  if (st.deploy?.error && !st.deploy.dry_run) return { tone: 'bad', label: 'Lần publish gần nhất lỗi — PROD vẫn chạy bản cũ' }
  if (!p.deployed) return { tone: 'ok', label: 'Chưa publish lần nào' }
  const dirty = st.dev.git?.dirty ?? 0
  if (st.dev_ahead > 0) return { tone: 'warn', label: `Đang live · DEV mới hơn PROD ${st.dev_ahead} commit — chưa publish` }
  if (st.dev_ahead === 0) {
    return dirty
      ? { tone: 'live', label: `Đang live · PROD = DEV, còn ${dirty} file sửa chưa commit` }
      : { tone: 'live', label: 'Đang live · PROD chạy đúng bản DEV' }
  }
  return { tone: 'live', label: 'Đang live' }
}

/**
 * InfraBadge is the room header's environments pill: PROD's state, where it
 * answers and which version, with DEV's light beside it. Hovering or focusing
 * it shows both environments; clicking opens the environments panel. The
 * provider's account, plan and traffic are not here — they are facts about the
 * provider, on Admin → Infra.
 */
export function InfraBadge({ st, err, onOpen }: { st: EnvStatus | null; err: string | null; onOpen: () => void }) {
  const prod = prodState(st)
  const dev = devState(st)
  const proj = projectState(st)
  const p = st?.prod
  const c = toneClass[err ? 'bad' : prod.tone]
  const where = prod.tone === 'live' ? host(p?.url) : ''
  const pv = prodVersion(st)
  const behind = (st?.dev_ahead ?? 0) > 0
  const summary = err
    ? `Không đọc được trạng thái môi trường: ${err}`
    : `PROD: ${prod.label}${where ? ` tại ${where}` : ''}${pv ? `, bản ${pv.text}` : ''}. DEV: ${dev.label}. ${proj.label}. Bấm để mở Môi trường.`

  return (
    <span className="relative group shrink-0">
      <button
        onClick={e => { e.currentTarget.blur(); onOpen() }}
        aria-label={summary}
        className={`flex items-center gap-1.5 @2xl:gap-2 h-7 pl-2 pr-2.5 rounded-full ring-1 ${c.ring} ${c.soft} text-[11px] cursor-pointer
          transition-colors duration-200 hover:brightness-110 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400`}
      >
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
        {pv && (
          <span className={`hidden @2xl:inline font-mono px-1.5 rounded-full ring-1 ${behind ? 'ring-amber-500/40 text-amber-200' : 'ring-gray-700 text-gray-300'}`}
            title={behind ? `PROD chậm ${st?.dev_ahead} commit so với DEV` : 'phiên bản đang chạy trên PROD'}>
            {pv.text}{behind ? ` · −${st?.dev_ahead}` : ''}
          </span>
        )}
        <span className="h-3 w-px bg-gray-700 hidden @3xl:block" aria-hidden />
        <span className="hidden @3xl:flex items-center gap-1 text-gray-400" title={`DEV: ${dev.label}`}>
          <Dot tone={dev.tone} size="h-1.5 w-1.5" />DEV
        </span>
      </button>

      {/* Both environments on hover or keyboard focus — never the only place
          a fact lives: the panel behind the click shows all of it. */}
      {st && !err && (
        <div
          role="tooltip"
          className="invisible opacity-0 translate-y-1 group-hover:visible group-hover:opacity-100 group-hover:translate-y-0
            group-focus-within:visible group-focus-within:opacity-100 group-focus-within:translate-y-0
            transition-all duration-150 motion-reduce:transition-none
            absolute left-0 top-full mt-2 z-40 w-80 max-w-[calc(100vw-2rem)] rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl p-3 space-y-2 text-xs"
        >
          <div className={`flex items-start gap-2 ${toneClass[proj.tone].text}`}>
            <span className="mt-1"><Dot tone={proj.tone} size="h-1.5 w-1.5" /></span>
            <span>{proj.label}</span>
          </div>
          <dl className="grid grid-cols-[44px_1fr] gap-x-2 gap-y-1.5 pt-1 border-t border-gray-800">
            <dt className="text-orange-300 font-semibold text-[10px] tracking-wider pt-px">PROD</dt>
            <dd className="min-w-0 text-gray-300">
              <span className={toneClass[prod.tone].text}>{prod.label}</span>
              {pv && <span className="font-mono text-gray-400"> · {pv.text}</span>}
              {p?.deployed_at && <span className="text-gray-500"> · {ago(p.deployed_at)}</span>}
              {p?.url && <div className="font-mono text-sky-300 truncate">{host(p.url)}</div>}
            </dd>
            <dt className="text-sky-300 font-semibold text-[10px] tracking-wider pt-px">DEV</dt>
            <dd className="min-w-0 text-gray-300">
              <span className={toneClass[dev.tone].text}>{dev.label}</span>
              {st.dev.git && <span className="font-mono text-gray-400"> · {devVersion(st)}{st.dev.git.branch ? ` (${st.dev.git.branch})` : ''}</span>}
            </dd>
          </dl>
        </div>
      )}
    </span>
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
