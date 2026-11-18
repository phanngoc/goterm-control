import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// Connectors, the way Lovable has them: what a project can plug into —
// Stripe, Resend, Gmail, OpenAI… A service is connected once for the machine
// (its keys go to the same store as the Connections page and never come back
// to the page), then enabled on a project, and the project's code gets the
// keys as environment variables in Preview and on Publish.
//
// Everything here is also a tool the agents have (`connector_*`, from
// `bomclaw mcp connectors`): "kết nối Stripe cho dự án này" in chat does what
// these buttons do. "Nhờ agent" posts exactly that ask in the project's room.

export interface FieldView {
  env: string; label: string; secret: boolean; required: boolean; public?: boolean; managed?: boolean
  placeholder?: string; help?: string
  set: boolean; source?: 'store' | 'env'; masked?: string; updated?: string; by?: string
}
export interface ConnectorView {
  id: string; title: string; purpose: string; docs?: string; category: string
  short: string; icon?: string; color?: string; popular?: boolean; auth: 'key' | 'oauth'
  requires?: string[]; hidden?: boolean; scopes?: string[]
  fields: FieldView[]; connected: boolean; enabled: boolean; ready: boolean
}
interface Category { id: string; title: string; blurb: string }
interface ListResult {
  categories: Category[]; connectors: ConnectorView[]
  project_dir?: string; missing?: Record<string, string>
}

const errText = (e: any) => String(e?.message ?? e)

/** useConnectors loads the catalog for a project (or the machine). */
export function useConnectors(call: Call, channelID?: string) {
  const [data, setData] = useState<ListResult | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const reload = useCallback(async () => {
    try {
      setData(await call('connectors.list', channelID ? { channel_id: channelID } : {}))
      setErr(null)
    } catch (e) {
      setErr(errText(e))
    }
  }, [call, channelID])
  useEffect(() => { void reload() }, [reload])
  return { data, err, reload }
}

// ── the button ────────────────────────────────────────────────────────────

export default function ConnectorsButton({ call, channelID, projectName, compact }: {
  call: Call; channelID: string; projectName?: string
  /** compact drops the label below a narrow container (the room header). */
  compact?: boolean
}) {
  const [open, setOpen] = useState(false)
  const { data, err, reload } = useConnectors(call, channelID)
  const enabled = data?.connectors.filter(c => c.enabled) ?? []
  const missing = Object.keys(data?.missing ?? {}).length
  return (
    <>
      <button
        onClick={() => { setOpen(true); void reload() }}
        title={enabled.length ? `Connectors: ${enabled.map(c => c.title).join(', ')}` : 'Connectors — Stripe, email, Google, AI…'}
        aria-label="Connectors"
        className="relative shrink-0 flex items-center gap-1.5 h-7 px-2.5 rounded-full text-xs cursor-pointer
          text-gray-300 ring-1 ring-gray-700 bg-gray-900/60 hover:bg-gray-800 hover:text-gray-100 transition-colors duration-150
          focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400"
      >
        <PlugIcon className="h-3.5 w-3.5" />
        <span className={compact ? 'hidden @xl:inline' : 'hidden md:inline'}>Connectors</span>
        {enabled.length > 0 && (
          <span className={`min-w-4 h-4 px-1 grid place-items-center rounded-full text-[10px] font-semibold
            ${missing ? 'bg-amber-500/25 text-amber-200' : 'bg-sky-500/20 text-sky-200'}`}>{enabled.length}</span>
        )}
      </button>
      {open && (
        <ConnectorsModal call={call} channelID={channelID} projectName={projectName}
          data={data} err={err} reload={reload} onClose={() => { setOpen(false); void reload() }} />
      )}
    </>
  )
}

// ── the modal ─────────────────────────────────────────────────────────────

type Nav = 'home' | 'all' | 'enabled' | string // or a category id

export function ConnectorsModal({ call, channelID, projectName, data, err, reload, onClose, inline }: {
  call: Call; channelID?: string; projectName?: string
  data: ListResult | null; err: string | null; reload: () => Promise<void>
  onClose?: () => void
  /** inline renders it as a page (Admin → Connectors) instead of a dialog. */
  inline?: boolean
}) {
  const [nav, setNav] = useState<Nav>('home')
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<string | null>(null)
  const search = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (inline) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (sel) setSel(null)
      else onClose?.()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [sel, onClose, inline])
  useEffect(() => { if (!inline) search.current?.focus() }, [inline])

  // A Google sign-in in its popup reports back here.
  useEffect(() => {
    const onMsg = (e: MessageEvent) => {
      if (e.origin === location.origin && e.data?.type === 'bomclaw-connector') void reload()
    }
    window.addEventListener('message', onMsg)
    return () => window.removeEventListener('message', onMsg)
  }, [reload])

  const all = useMemo(() => (data?.connectors ?? []).filter(c => !c.hidden), [data])
  const byId = useMemo(() => Object.fromEntries((data?.connectors ?? []).map(c => [c.id, c])), [data])
  const cats = data?.categories ?? []
  const enabledN = all.filter(c => c.enabled).length
  const connectedN = all.filter(c => c.ready).length

  const query = q.trim().toLowerCase()
  const matches = (c: ConnectorView) => !query ||
    [c.title, c.id, c.short, c.purpose, c.category].some(s => s?.toLowerCase().includes(query)) ||
    c.fields.some(f => f.env.toLowerCase().includes(query))

  const selected = sel ? byId[sel] : null

  const shell = (
    <div className={`flex ${inline ? 'h-full' : 'h-[min(46rem,calc(100vh-3rem))]'} w-full overflow-hidden bg-gray-950 text-gray-200
      ${inline ? '' : 'rounded-2xl ring-1 ring-gray-800 shadow-2xl'}`}>
      {/* Left rail — Lovable's: Home, All, Enabled, then the sections. */}
      <nav aria-label="Danh mục connector" className="hidden sm:flex w-52 shrink-0 flex-col gap-0.5 p-3 border-r border-gray-800 bg-gray-900/40">
        <RailItem on={nav === 'home' && !query} onClick={() => { setNav('home'); setQ(''); setSel(null) }}>Home</RailItem>
        <RailItem on={nav === 'all' && !query} onClick={() => { setNav('all'); setQ(''); setSel(null) }}>
          Tất cả<span className="ml-auto text-[11px] text-gray-500">{all.length}</span>
        </RailItem>
        <RailItem on={nav === 'enabled' && !query} onClick={() => { setNav('enabled'); setQ(''); setSel(null) }}>
          {channelID ? 'Đã bật' : 'Đã kết nối'}
          <span className="ml-auto text-[11px] text-gray-500">{channelID ? enabledN : connectedN}</span>
        </RailItem>
        <div className="my-2 border-t border-gray-800" />
        {cats.map(c => (
          <RailItem key={c.id} on={nav === c.id && !query} onClick={() => { setNav(c.id); setQ(''); setSel(null) }}>
            <span className="text-gray-500">{CatIcon[c.id] ?? '•'}</span>{c.title}
          </RailItem>
        ))}
        <div className="mt-auto pt-3 text-[11px] leading-relaxed text-gray-500">
          Nói với agent cũng được: <span className="text-gray-400">“kết nối Stripe cho dự án này”</span> — agent có cùng các
          nút này dưới dạng tool <span className="font-mono">connector_*</span>.
        </div>
      </nav>

      <div className="flex-1 min-w-0 flex flex-col">
        <header className="flex items-center gap-3 h-14 px-4 border-b border-gray-800 shrink-0">
          <PlugIcon className="h-4 w-4 text-gray-400" />
          <div className="min-w-0">
            <div className="text-sm font-semibold text-gray-50">Connectors</div>
            {channelID && <div className="text-[11px] text-gray-500 truncate">cho dự án {projectName || channelID}</div>}
          </div>
          <div className="ml-auto flex items-center gap-2 min-w-0 flex-1 justify-end">
            <label className="relative w-full max-w-sm">
              <span className="sr-only">Tìm connector</span>
              <SearchIcon className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-gray-500" />
              <input ref={search} value={q} onChange={e => { setQ(e.target.value); setSel(null) }}
                placeholder="Tìm: thanh toán, email, Google Sheets…"
                className="w-full h-8 pl-8 pr-3 rounded-full text-sm bg-gray-900 ring-1 ring-gray-800 text-gray-200 placeholder:text-gray-500 outline-none focus:ring-sky-500/60" />
            </label>
            {onClose && !inline && (
              <button onClick={onClose} aria-label="Đóng" className="h-8 w-8 grid place-items-center rounded-full text-gray-400 hover:text-gray-100 hover:bg-gray-800 cursor-pointer">✕</button>
            )}
          </div>
        </header>
        {/* Phone: the rail as chips. */}
        <div className="sm:hidden flex gap-1.5 overflow-x-auto px-3 py-2 border-b border-gray-800 shrink-0">
          {([['home', 'Home'], ['all', 'Tất cả'], ['enabled', channelID ? 'Đã bật' : 'Đã kết nối'], ...cats.map(c => [c.id, c.title])] as [string, string][]).map(([id, t]) => (
            <button key={id} onClick={() => { setNav(id); setQ(''); setSel(null) }}
              className={`shrink-0 h-7 px-3 rounded-full text-xs ${nav === id ? 'bg-gray-800 text-gray-100' : 'text-gray-400 ring-1 ring-gray-800'}`}>{t}</button>
          ))}
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto">
          {err && <div className="m-4 text-xs text-red-300">{err}</div>}
          {!data && !err && <div className="m-4 grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
            {Array.from({ length: 9 }).map((_, i) => <div key={i} className="h-[4.5rem] rounded-xl bg-gray-900 motion-safe:animate-pulse" />)}
          </div>}
          {data && selected ? (
            <Detail key={selected.id} call={call} c={selected} byId={byId} channelID={channelID} projectName={projectName}
              missing={data.missing?.[selected.id]} reload={reload} onBack={() => setSel(null)} />
          ) : data && (
            <div className="p-4 space-y-6">
              {query ? (
                <Section title={`Kết quả cho “${q.trim()}”`} items={all.filter(matches)} onPick={setSel} channel={!!channelID}
                  empty="Không có connector nào khớp. Agent vẫn tích hợp được dịch vụ khác bằng API key — hỏi trong chat." />
              ) : nav === 'home' ? (
                <>
                  {channelID && enabledN > 0 && <Section title="Đang bật cho dự án" items={all.filter(c => c.enabled)} onPick={setSel} channel />}
                  <Section title="Phổ biến" items={all.filter(c => c.popular)} onPick={setSel} channel={!!channelID} onMore={() => setNav('all')} />
                  {cats.map(cat => (
                    <Section key={cat.id} title={cat.title} sub={cat.blurb} items={all.filter(c => c.category === cat.id)}
                      onPick={setSel} channel={!!channelID} onMore={() => setNav(cat.id)} limit={6} />
                  ))}
                </>
              ) : nav === 'all' ? (
                <Section title="Tất cả connector" items={all} onPick={setSel} channel={!!channelID} />
              ) : nav === 'enabled' ? (
                <Section title={channelID ? 'Đã bật cho dự án' : 'Đã kết nối trên máy này'}
                  items={all.filter(c => (channelID ? c.enabled : c.ready))} onPick={setSel} channel={!!channelID}
                  empty={channelID ? 'Chưa bật connector nào. Chọn một dịch vụ, hoặc nhờ agent: “thêm thanh toán Stripe”.' : 'Chưa kết nối dịch vụ nào.'} />
              ) : (
                <Section title={cats.find(c => c.id === nav)?.title ?? nav} sub={cats.find(c => c.id === nav)?.blurb}
                  items={all.filter(c => c.category === nav)} onPick={setSel} channel={!!channelID} />
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )

  if (inline) return shell
  return (
    <div className="fixed inset-0 z-[60] grid place-items-center p-3 sm:p-6 bg-black/60 backdrop-blur-[2px]"
      onMouseDown={e => { if (e.target === e.currentTarget) onClose?.() }}>
      <div role="dialog" aria-modal="true" aria-label="Connectors" className="w-full max-w-6xl">{shell}</div>
    </div>
  )
}

function RailItem({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} aria-current={on ? 'page' : undefined}
      className={`flex items-center gap-2 h-8 px-2.5 rounded-lg text-sm text-left cursor-pointer transition-colors duration-150
        ${on ? 'bg-gray-800 text-gray-50' : 'text-gray-400 hover:text-gray-100 hover:bg-gray-800/50'}`}>
      {children}
    </button>
  )
}

function Section({ title, sub, items, onPick, channel, onMore, limit, empty }: {
  title: string; sub?: string; items: ConnectorView[]; onPick: (id: string) => void; channel: boolean
  onMore?: () => void; limit?: number; empty?: string
}) {
  const shown = limit ? items.slice(0, limit) : items
  return (
    <section>
      <div className="flex items-baseline gap-2 mb-2.5">
        {onMore ? (
          <button onClick={onMore} className="text-[15px] font-semibold text-gray-100 hover:text-white cursor-pointer">{title} <span className="text-gray-500">›</span></button>
        ) : <h3 className="text-[15px] font-semibold text-gray-100">{title}</h3>}
        {sub && <span className="text-xs text-gray-500 truncate">{sub}</span>}
      </div>
      {shown.length === 0 ? (
        <div className="text-xs text-gray-500">{empty ?? '—'}</div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-2.5">
          {shown.map(c => <Card key={c.id} c={c} onPick={onPick} channel={channel} />)}
        </div>
      )}
    </section>
  )
}

function Card({ c, onPick, channel }: { c: ConnectorView; onPick: (id: string) => void; channel: boolean }) {
  return (
    <button onClick={() => onPick(c.id)}
      className="group flex items-center gap-3 p-3 rounded-xl text-left cursor-pointer bg-gray-900/50 ring-1 ring-gray-800
        hover:ring-gray-600 hover:bg-gray-900 transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400">
      <Logo c={c} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="text-sm font-medium text-gray-100 truncate">{c.title}</span>
          {c.ready && <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 shrink-0" title="đã kết nối" />}
        </div>
        <div className="text-xs text-gray-500 truncate">{c.short}</div>
      </div>
      {channel && c.enabled && (
        <span className="shrink-0 px-1.5 h-5 grid place-items-center rounded-full text-[10px] font-medium bg-sky-500/15 text-sky-200 ring-1 ring-sky-500/30">đã bật</span>
      )}
    </button>
  )
}

/** Logo is the brand's mark from simpleicons, on white like Lovable's tiles;
 *  a letter in the brand color when it cannot load (offline, no icon). */
function Logo({ c, size = 'h-10 w-10' }: { c: ConnectorView; size?: string }) {
  const [broken, setBroken] = useState(!c.icon)
  const color = c.color || '6b7280'
  return (
    <span className={`${size} shrink-0 grid place-items-center rounded-lg bg-white ring-1 ring-black/10 overflow-hidden`}>
      {broken ? (
        <span className="text-base font-bold" style={{ color: `#${color}` }}>{c.title[0]}</span>
      ) : (
        <img src={`https://cdn.simpleicons.org/${c.icon}/${color}`} alt="" loading="lazy" className="h-[55%] w-[55%]"
          onError={() => setBroken(true)} />
      )}
    </span>
  )
}

// ── one connector ─────────────────────────────────────────────────────────

const input = 'w-full px-2.5 py-1.5 text-sm bg-gray-900 rounded-lg ring-1 ring-gray-800 text-gray-200 outline-none focus:ring-sky-500/60 font-mono placeholder:text-gray-600'
const btn = 'h-8 px-3 rounded-lg text-xs font-medium ring-1 transition-colors duration-150 cursor-pointer disabled:opacity-40 disabled:cursor-default'

function Detail({ call, c, byId, channelID, projectName, missing, reload, onBack }: {
  call: Call; c: ConnectorView; byId: Record<string, ConnectorView>; channelID?: string; projectName?: string
  missing?: string; reload: () => Promise<void>; onBack: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [check, setCheck] = useState<{ ok: boolean; detail: string } | null>(null)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [asked, setAsked] = useState(false)
  const reqs = (c.requires ?? []).map(id => byId[id]).filter(Boolean)
  const stored = c.fields.some(f => f.source === 'store')

  // A sign-in that just finished: check it.
  useEffect(() => {
    const onMsg = (e: MessageEvent) => {
      if (e.origin !== location.origin || e.data?.type !== 'bomclaw-connector' || e.data.id !== c.id) return
      setCheck({ ok: !!e.data.ok, detail: e.data.message })
    }
    window.addEventListener('message', onMsg)
    return () => window.removeEventListener('message', onMsg)
  }, [c.id])

  const verify = async () => {
    setBusy(true)
    try { setCheck(await call('connectors.verify', { id: c.id })) } catch (e) { setCheck({ ok: false, detail: errText(e) }) }
    finally { setBusy(false) }
  }
  const toggle = async (on: boolean) => {
    if (!channelID) return
    setBusy(true); setMsg(null)
    try {
      await call('connectors.enable', { channel_id: channelID, id: c.id, on })
      await reload()
      setMsg({ ok: true, text: on ? 'Đã bật — Preview đang mở sẽ tự chạy lại với khóa mới; PROD nhận ở lần Publish tới.' : 'Đã tắt cho dự án này.' })
    } catch (e) { setMsg({ ok: false, text: errText(e) }) }
    finally { setBusy(false) }
  }
  const disconnect = async () => {
    if (!confirm(`Xóa khóa ${c.title} đã lưu trên máy này? Mọi dự án đang bật nó sẽ mất kết nối. (Giá trị trong .env không bị đụng tới.)`)) return
    try { await call('connectors.unset', { id: c.id }); setCheck(null); await reload() } catch (e) { setMsg({ ok: false, text: errText(e) }) }
  }
  const askAgent = async () => {
    if (!channelID) return
    try {
      const chans: any[] = (await call('channels.list')) || []
      const ch = chans.find(x => x.id === channelID)
      const agent = ch?.members?.find((m: any) => m.kind === 'agent')?.id || 'bomclaw'
      const state = c.ready ? `${c.title} đã kết nối trên máy.` : `${c.title} chưa kết nối — hỏi mình khóa${c.auth === 'oauth' ? ' / gửi link đăng nhập Google' : ''}.`
      await call('channels.post', {
        channel_id: channelID, notify: [agent],
        body: `@${agent} tích hợp ${c.title} vào dự án này: dùng tool connector_enable id=${c.id}, đọc connector_guide id=${c.id} rồi viết code dùng nó (khóa chỉ đọc từ biến môi trường, không ghi vào code). ${state}`,
      })
      setAsked(true)
    } catch (e) { setMsg({ ok: false, text: errText(e) }) }
  }

  return (
    <div className="p-4 sm:p-6 max-w-3xl space-y-5">
      <button onClick={onBack} className="text-xs text-gray-400 hover:text-gray-100 cursor-pointer">‹ Tất cả connector</button>

      <div className="flex items-start gap-4">
        <Logo c={c} size="h-14 w-14" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <h2 className="text-lg font-semibold text-gray-50">{c.title}</h2>
            <span className={`inline-flex items-center gap-1.5 text-[11px] ${c.ready ? 'text-emerald-300' : 'text-gray-500'}`}>
              <span className={`h-1.5 w-1.5 rounded-full ${c.ready ? 'bg-emerald-400' : 'bg-gray-600'}`} />
              {c.ready ? 'đã kết nối' : 'chưa kết nối'}
            </span>
          </div>
          <p className="text-sm text-gray-400 mt-0.5">{c.purpose}</p>
          {c.docs && <a href={c.docs} target="_blank" rel="noreferrer" className="inline-block mt-1 text-xs text-sky-400 hover:underline">Lấy khóa ở đâu ↗</a>}
        </div>
      </div>

      {/* On this project. */}
      {channelID && (
        <div className="flex items-center gap-3 rounded-xl bg-gray-900/60 ring-1 ring-gray-800 p-3">
          <div className="min-w-0 flex-1">
            <div className="text-sm text-gray-100">Dùng trong dự án <span className="font-medium">{projectName || channelID}</span></div>
            <div className="text-xs text-gray-500">
              {c.enabled
                ? (missing ? <span className="text-amber-300">Đã bật nhưng chưa có khóa: {missing}</span> : 'Code của dự án nhận khóa qua biến môi trường — Preview và PROD.')
                : 'Bật để code của dự án đọc được khóa của dịch vụ này.'}
            </div>
          </div>
          <Switch on={c.enabled} disabled={busy} onChange={toggle} label={`Bật ${c.title} cho dự án`} />
        </div>
      )}

      {/* Prerequisite: the Google OAuth client. */}
      {reqs.map(r => <KeysForm key={r.id} call={call} c={r} reload={reload} title={`Bước 1 · ${r.title}`} note={
        <>Một lần cho mọi connector Google. Authorized redirect URI:{' '}
          <CopyText text={`${location.origin}/api/connectors/oauth/callback`} /></>
      } />)}

      {c.auth === 'oauth' ? (
        <div className="rounded-xl ring-1 ring-gray-800 p-4 space-y-3">
          <div className="text-sm font-medium text-gray-100">{reqs.length ? 'Bước 2 · ' : ''}Đăng nhập Google</div>
          <p className="text-xs text-gray-400">
            Quyền xin: {(c.scopes ?? []).filter(s => s.startsWith('https://')).map(s => s.replace('https://www.googleapis.com/auth/', '')).join(', ')}.
            App dùng tài khoản đăng nhập ở đây cho mọi người dùng của nó.
          </p>
          {c.fields.filter(f => f.managed && f.set).map(f => (
            <div key={f.env} className="text-xs text-gray-500">{f.env}: <span className="font-mono text-gray-400">{f.masked}</span>
              {f.updated && ` · ${ago(f.updated)}`}</div>
          ))}
          <button
            disabled={reqs.some(r => !r.connected)}
            onClick={() => window.open(`/api/connectors/oauth/start?id=${encodeURIComponent(c.id)}${channelID ? `&project=${encodeURIComponent(channelID)}` : ''}`,
              'bomclaw-google', 'width=520,height=680')}
            className={`${btn} h-9 px-4 bg-white text-black ring-black/20 hover:brightness-95 inline-flex items-center gap-2`}>
            <GoogleG /> {c.connected ? 'Kết nối lại với Google' : 'Kết nối với Google'}
          </button>
          {reqs.some(r => !r.connected) && <div className="text-[11px] text-amber-300">Nhập Google OAuth client ở bước 1 trước.</div>}
        </div>
      ) : (
        <KeysForm call={call} c={c} reload={reload} onSaved={verify} />
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button disabled={busy || !c.connected} onClick={verify} className={`${btn} ring-gray-700 text-gray-300 hover:bg-gray-800`}>
          {busy ? 'Đang kiểm tra…' : 'Kiểm tra kết nối'}
        </button>
        {channelID && (
          <button onClick={askAgent} disabled={asked}
            className={`${btn} text-white ring-violet-400/50 bg-gradient-to-b from-violet-500 to-violet-600 hover:brightness-110`}>
            {asked ? '✓ Đã nhờ agent — xem trong phòng' : 'Nhờ agent tích hợp vào app'}
          </button>
        )}
        {stored && <button onClick={disconnect} className={`${btn} ring-transparent text-gray-500 hover:text-red-300`}>Ngắt kết nối</button>}
      </div>
      {check && (
        <div role="status" className={`text-xs rounded-lg px-3 py-2 ${check.ok ? 'bg-emerald-500/10 text-emerald-300' : 'bg-red-500/10 text-red-300'}`}>
          {check.ok ? '✓ ' : '✗ '}{check.detail}
        </div>
      )}
      {msg && <div role="status" className={`text-xs ${msg.ok ? 'text-emerald-300' : 'text-red-300'}`}>{msg.ok ? '✓ ' : '✗ '}{msg.text}</div>}

      {/* What the code gets. */}
      <div className="rounded-xl bg-gray-900/40 ring-1 ring-gray-800 p-3">
        <div className="text-xs font-medium text-gray-300 mb-2">Biến môi trường code nhận được</div>
        <ul className="space-y-1">
          {[...reqs.flatMap(r => r.fields), ...c.fields].map(f => (
            <li key={f.env} className="flex items-center gap-2 text-xs">
              <code className="font-mono text-gray-200">{f.env}</code>
              {f.public
                ? <span className="px-1.5 rounded-full text-[10px] bg-sky-500/10 text-sky-300 ring-1 ring-sky-500/30">public · cả VITE_{f.env}</span>
                : <span className="px-1.5 rounded-full text-[10px] bg-amber-500/10 text-amber-300 ring-1 ring-amber-500/30">bí mật · chỉ server</span>}
              {!f.set && f.required && <span className="text-gray-600">chưa có</span>}
            </li>
          ))}
        </ul>
        <p className="mt-2 text-[11px] text-gray-500">
          Khóa bí mật chỉ dùng ở server: Worker script (<span className="font-mono">env.NAME</span>) hoặc Node (<span className="font-mono">process.env.NAME</span>).
          Site tĩnh chỉ nhận giá trị public.
        </p>
      </div>
    </div>
  )
}

/** KeysForm is a service's fields: masked when stored, empty keeps them. */
function KeysForm({ call, c, reload, onSaved, title, note }: {
  call: Call; c: ConnectorView; reload: () => Promise<void>; onSaved?: () => void
  title?: string; note?: React.ReactNode
}) {
  const [values, setValues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const fields = c.fields.filter(f => !f.managed)
  const dirty = Object.values(values).some(v => v.trim() !== '')
  const save = async () => {
    setBusy(true); setErr(null)
    try {
      await call('connectors.set', { id: c.id, values })
      setValues({})
      await reload()
      onSaved?.()
    } catch (e) { setErr(errText(e)) }
    finally { setBusy(false) }
  }
  return (
    <div className="rounded-xl ring-1 ring-gray-800 p-4 space-y-3">
      <div className="flex items-center gap-2">
        <div className="text-sm font-medium text-gray-100">{title ?? 'Khóa'}</div>
        {c.connected && <span className="text-[11px] text-emerald-300">✓ đã lưu</span>}
        {c.docs && title && <a href={c.docs} target="_blank" rel="noreferrer" className="ml-auto text-[11px] text-sky-400 hover:underline">mở Google Cloud ↗</a>}
      </div>
      {note && <div className="text-[11px] text-gray-400">{note}</div>}
      {fields.map(f => (
        <label key={f.env} className="block">
          <div className="flex items-baseline gap-2 text-[11px]">
            <span className="text-gray-300">{f.label}{f.required && <span className="text-amber-400"> *</span>}</span>
            <span className="font-mono text-gray-600">{f.env}</span>
            {f.set && (
              <span className="ml-auto text-gray-500">
                {f.source === 'env' ? 'từ .env' : `đã lưu${f.by ? ` · ${f.by}` : ''}${f.updated ? ` · ${ago(f.updated)}` : ''}`}
              </span>
            )}
          </div>
          <input
            type={f.secret ? 'password' : 'text'} autoComplete="off" spellCheck={false}
            value={values[f.env] ?? ''}
            onChange={e => setValues(v => ({ ...v, [f.env]: e.target.value }))}
            placeholder={f.set ? `${f.masked} — để trống để giữ nguyên` : (f.placeholder || '')}
            className={`${input} mt-1`}
          />
          {f.help && <div className="mt-0.5 text-[11px] text-gray-500">{f.help}</div>}
        </label>
      ))}
      <div className="flex items-center gap-2">
        <button disabled={busy || !dirty} onClick={save} className={`${btn} ring-sky-500/40 bg-sky-500/15 text-sky-200 hover:bg-sky-500/25`}>
          {busy ? 'Đang lưu…' : onSaved ? 'Lưu & kiểm tra' : 'Lưu'}
        </button>
        <span className="text-[11px] text-gray-600">Không bao giờ hiện lại đầy đủ.</span>
      </div>
      {err && <div className="text-xs text-red-300">{err}</div>}
    </div>
  )
}

function Switch({ on, onChange, disabled, label }: { on: boolean; onChange: (v: boolean) => void; disabled?: boolean; label: string }) {
  return (
    <button role="switch" aria-checked={on} aria-label={label} disabled={disabled} onClick={() => onChange(!on)}
      className={`relative shrink-0 h-6 w-11 rounded-full transition-colors duration-150 cursor-pointer disabled:opacity-50
        focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400 ${on ? 'bg-sky-500' : 'bg-gray-700'}`}>
      <span className={`absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition-[left] duration-150 ${on ? 'left-[1.375rem]' : 'left-0.5'}`} />
    </button>
  )
}

function CopyText({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <button onClick={() => { void navigator.clipboard?.writeText(text); setDone(true); setTimeout(() => setDone(false), 1500) }}
      title="Chép" className="font-mono text-gray-200 hover:text-sky-300 cursor-pointer break-all text-left">
      {text} <span className="text-gray-500">{done ? '✓' : '⧉'}</span>
    </button>
  )
}

const CatIcon: Record<string, string> = { grow: '↗', sell: '▭', communicate: '◌', work: '▢', ai: '✦' }

export function PlugIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" className={className} aria-hidden>
      <path d="M7 2.75v3.5M13 2.75v3.5M5 6.25h10v3a5 5 0 0 1-10 0v-3ZM10 14.25v3" />
    </svg>
  )
}

function SearchIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" className={className} aria-hidden>
      <circle cx="9" cy="9" r="5.5" /><path d="m13.25 13.25 3.5 3.5" />
    </svg>
  )
}

function GoogleG() {
  return (
    <svg viewBox="0 0 48 48" className="h-4 w-4" aria-hidden>
      <path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9.1 3.6l6.8-6.8C35.8 2.4 30.3 0 24 0 14.6 0 6.6 5.4 2.7 13.3l7.9 6.1C12.5 13.6 17.8 9.5 24 9.5z" />
      <path fill="#4285F4" d="M46.1 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.4c-.5 2.9-2.2 5.3-4.6 6.9l7.4 5.8c4.3-4 6.9-9.9 6.9-17.2z" />
      <path fill="#FBBC05" d="M10.6 28.6c-.5-1.4-.8-2.9-.8-4.6s.3-3.2.8-4.6l-7.9-6.1C1 16.6 0 20.2 0 24s1 7.4 2.7 10.7l7.9-6.1z" />
      <path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.4-5.8c-2.1 1.4-4.8 2.3-8.5 2.3-6.2 0-11.5-4.1-13.4-9.9l-7.9 6.1C6.6 42.6 14.6 48 24 48z" />
    </svg>
  )
}
