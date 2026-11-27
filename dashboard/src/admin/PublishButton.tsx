import { useEffect, useRef, useState } from 'react'
import { ago } from './format'
import { CloudIcon, Dot, host, prodState, prodVersion, toneClass, useEnvStatus } from './InfraBadge'

type Call = (method: string, params?: any) => Promise<any>

// Publish, the way Lovable puts it: one primary button at the top right of
// the editor, and a popover that says where the project is live, when it last
// went out, and one button that sends the current version there. The work is
// env.deploy — the same code the agent's `publish` MCP tool runs — and a real
// publish is announced in the project's room either way.
//
// A project that cannot publish yet (no "deploy" in bomclaw.json) is not a
// dead end here: the popover asks an agent to set it up, which it does with
// the publish_setup tool.

export default function PublishButton({ call, channelID, onDetails, compact }: {
  call: Call; channelID: string; onDetails?: () => void
  /** compact drops the label below a narrow container (the room header). */
  compact?: boolean
}) {
  const [open, setOpen] = useState(false)
  const { st, err, reload } = useEnvStatus(call, channelID)
  const [logs, setLogs] = useState('')
  const [withDB, setWithDB] = useState(false)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [asked, setAsked] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const logBox = useRef<HTMLPreElement>(null)

  const running = !!st?.deploy?.running
  const p = st?.prod
  const prod = prodState(st)
  const pv = prodVersion(st)
  const behind = (st?.dev_ahead ?? 0) > 0
  const live = !!p?.deployed

  // Close on a click outside or Escape.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('mousedown', onDown); document.removeEventListener('keydown', onKey) }
  }, [open])

  // Follow the log while a publish runs (started here or by an agent's tool
  // in this process — the status says which).
  useEffect(() => {
    if (!open && !running) return
    let stop = false
    const tick = async () => {
      try {
        const l = await call('env.logs', { channel_id: channelID })
        if (!stop) setLogs(l?.logs ?? '')
      } catch { /* the status line already says what is wrong */ }
    }
    void tick()
    const t = setInterval(tick, running ? 1500 : 10000)
    return () => { stop = true; clearInterval(t) }
  }, [open, running, call, channelID])
  useEffect(() => { if (logBox.current) logBox.current.scrollTop = logBox.current.scrollHeight }, [logs])

  // A publish that just ended says how it went.
  const wasRunning = useRef(false)
  useEffect(() => {
    if (wasRunning.current && !running && st?.deploy) {
      const d = st.deploy
      setMsg(d.error ? { ok: false, text: 'Publish lỗi — xem log' }
        : d.dry_run ? { ok: true, text: 'Dry run xong — chưa publish gì' }
        : { ok: true, text: `Đã publish${d.result?.url ? ' → ' + host(d.result.url) : ''}` })
    }
    wasRunning.current = running
  }, [running, st?.deploy])

  const publish = async (dryRun = false) => {
    if (!dryRun && withDB && !confirm('Tải database local lên sẽ THAY THẾ toàn bộ bảng trên PROD. Tiếp tục?')) return
    setBusy(true); setMsg(null)
    try {
      await call('env.deploy', { channel_id: channelID, dry_run: dryRun, with_db: withDB })
      await reload()
    } catch (e: any) {
      setMsg({ ok: false, text: String(e?.message ?? e) })
    } finally {
      setBusy(false)
    }
  }

  // Not set up: an agent does it, with the publish_setup tool.
  const askAgent = async () => {
    try {
      const chans: any[] = (await call('channels.list')) || []
      const ch = chans.find(c => c.id === channelID)
      const agent = ch?.members?.find((m: any) => m.kind === 'agent')?.id || 'bomclaw'
      const found = p?.deployed && p?.name ? ` Dự án đang live ở Worker ${p.name}${p.url ? ` (${host(p.url)})` : ''} — giữ nguyên tên và domain đó.` : ''
      await call('channels.post', {
        channel_id: channelID, notify: [agent],
        body: `@${agent} thiết lập Publish cho dự án này bằng tool publish_setup (thư mục build, lệnh build, domain), rồi chạy publish và gửi link.${found}`,
      })
      setAsked(true)
    } catch (e: any) {
      setMsg({ ok: false, text: String(e?.message ?? e) })
    }
  }

  const label = running ? 'Đang publish…' : live ? 'Publish' : 'Publish'
  const canPublish = !!st?.declared && !!p?.connected

  return (
    <div ref={box} className="relative shrink-0">
      <button
        onClick={() => setOpen(!open)}
        aria-haspopup="dialog" aria-expanded={open} aria-label={label}
        className="flex items-center gap-1.5 h-7 pl-2.5 pr-3 rounded-full text-xs font-semibold cursor-pointer text-white
          bg-gradient-to-b from-violet-500 to-violet-600 shadow-sm shadow-violet-900/40 ring-1 ring-violet-400/50
          transition-[filter,transform] duration-150 hover:brightness-110 active:scale-[0.98]
          focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-violet-300"
      >
        {running
          ? <span className="h-3.5 w-3.5 rounded-full border-2 border-white/40 border-t-white motion-safe:animate-spin" aria-hidden />
          : <GlobeIcon className="h-3.5 w-3.5" />}
        <span className={compact ? 'hidden @xl:inline' : ''}>{label}</span>
        {live && !running && <span className="h-1.5 w-1.5 rounded-full bg-emerald-300" title="đang live" aria-hidden />}
      </button>

      {open && (
        <div role="dialog" aria-label="Publish"
          className="absolute right-0 top-full mt-2 z-50 w-[380px] max-w-[calc(100vw-1.5rem)] rounded-2xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl text-gray-200 overflow-hidden">
          <div className="p-4 space-y-3">
            <div className="flex items-center gap-2">
              <span className="text-sm font-semibold text-gray-50">Publish</span>
              <span className={`ml-auto inline-flex items-center gap-1.5 text-[11px] ${toneClass[prod.tone].text}`}>
                <Dot tone={prod.tone} size="h-1.5 w-1.5" />{prod.tone === 'live' ? 'đang live' : prod.label}
              </span>
            </div>

            {/* Where it lives. */}
            <div className="rounded-xl bg-gray-900/60 ring-1 ring-gray-800 p-3 space-y-2">
              <div className="flex items-center gap-2 text-xs">
                <CloudIcon className="h-4 w-4 text-gray-400" />
                <span className="text-gray-300">{p?.title || p?.provider || 'Cloudflare'}</span>
                {pv && <span className="ml-auto font-mono text-[11px] px-1.5 rounded ring-1 ring-gray-700 text-gray-300" title="phiên bản đang chạy trên PROD">{pv.text}</span>}
              </div>
              {p?.url ? (
                <div className="flex items-center gap-2">
                  <a href={p.url} target="_blank" rel="noopener noreferrer"
                    className="flex-1 min-w-0 truncate font-mono text-[13px] text-sky-300 hover:underline">{host(p.url)}</a>
                  <button onClick={() => navigator.clipboard?.writeText(p.url!)} aria-label="Chép địa chỉ"
                    className="h-7 px-2 rounded-lg text-[11px] text-gray-400 ring-1 ring-gray-700 cursor-pointer hover:text-gray-100 hover:bg-gray-800">chép</button>
                  <a href={p.url} target="_blank" rel="noopener noreferrer" aria-label="Mở site"
                    className="h-7 px-2 grid place-items-center rounded-lg text-[11px] text-gray-400 ring-1 ring-gray-700 hover:text-gray-100 hover:bg-gray-800">mở ↗</a>
                </div>
              ) : (
                <div className="text-xs text-gray-500">{p?.connected ? 'Chưa có địa chỉ public — chưa publish lần nào.' : '—'}</div>
              )}
              {p?.deployed_at && <div className="text-[11px] text-gray-500">Publish gần nhất {ago(p.deployed_at)}{p.discovered_from ? ' · deploy ngoài dashboard' : ''}</div>}
              {behind && <div className="text-[11px] text-amber-300">DEV mới hơn PROD {st?.dev_ahead} commit — chưa publish</div>}
              {p?.want_domain && !(p.domains ?? []).includes(p.want_domain) && (
                <div className="text-[11px] text-amber-300">{p.want_domain} sẽ được gắn ở lần publish tới</div>
              )}
            </div>

            {err && <div className="text-xs text-red-300">{err}</div>}

            {/* What to do. */}
            {!st ? (
              <div className="h-9 rounded-xl bg-gray-800 motion-safe:animate-pulse" aria-label="đang kiểm tra" />
            ) : !p?.connected ? (
              <a href="/admin/connections"
                className="flex items-center justify-center h-9 rounded-xl text-sm font-medium bg-amber-500/15 text-amber-200 ring-1 ring-amber-500/40 hover:bg-amber-500/25">
                Kết nối {p?.title || 'Cloudflare'} để publish ▸
              </a>
            ) : !st.declared ? (
              <div className="space-y-2">
                <p className="text-xs text-gray-400">
                  {live ? 'Site đang live nhưng chưa thiết lập publish từ đây.' : 'Dự án chưa thiết lập publish.'} Agent
                  thiết lập được bằng tool <span className="font-mono text-gray-300">publish_setup</span> rồi publish luôn.
                </p>
                <button onClick={askAgent} disabled={asked}
                  className="w-full h-9 rounded-xl text-sm font-semibold text-white cursor-pointer bg-gradient-to-b from-violet-500 to-violet-600 ring-1 ring-violet-400/50 hover:brightness-110 disabled:opacity-60 disabled:cursor-default">
                  {asked ? '✓ Đã nhờ agent — xem trong phòng' : 'Nhờ agent thiết lập & publish'}
                </button>
              </div>
            ) : (
              <div className="space-y-2">
                <button onClick={() => publish(false)} disabled={running || busy || !canPublish}
                  className="w-full h-9 rounded-xl text-sm font-semibold text-white cursor-pointer bg-gradient-to-b from-violet-500 to-violet-600 ring-1 ring-violet-400/50 transition-[filter] hover:brightness-110 disabled:opacity-60 disabled:cursor-not-allowed">
                  {running ? 'Đang publish…' : live ? 'Cập nhật bản mới' : 'Publish lần đầu'}
                </button>
                <div className="flex items-center gap-3 text-[11px] text-gray-400">
                  <button onClick={() => publish(true)} disabled={running || busy}
                    className="cursor-pointer hover:text-gray-100 disabled:opacity-50" title="Build và kiểm tra, không publish">dry run</button>
                  {st.spec?.database && (
                    <label className="flex items-center gap-1.5 cursor-pointer">
                      <input type="checkbox" checked={withDB} onChange={e => setWithDB(e.target.checked)} className="accent-orange-400" />
                      kèm database <span className="text-orange-300">(thay bảng PROD)</span>
                    </label>
                  )}
                </div>
              </div>
            )}

            {msg && <div role="status" className={`text-xs ${msg.ok ? 'text-emerald-300' : 'text-red-300'}`}>{msg.ok ? '✓ ' : '✗ '}{msg.text}</div>}

            {(running || logs) && (
              <pre ref={logBox}
                className="max-h-40 overflow-auto rounded-lg bg-gray-900 ring-1 ring-gray-800 p-2 text-[10.5px] leading-relaxed font-mono text-gray-400 whitespace-pre-wrap">
                {logs || '…'}
              </pre>
            )}
          </div>
          <div className="flex items-center gap-3 px-4 h-10 border-t border-gray-800 text-[11px] text-gray-500">
            <span>Agent cũng publish được bằng tool <span className="font-mono text-gray-400">publish</span></span>
            {onDetails && (
              <button onClick={() => { setOpen(false); onDetails() }} className="ml-auto text-sky-300 cursor-pointer hover:underline">Môi trường ▸</button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function GlobeIcon({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" className={className} aria-hidden>
      <circle cx="10" cy="10" r="7.25" />
      <path d="M2.75 10h14.5M10 2.75c2 2.1 3 4.5 3 7.25s-1 5.15-3 7.25c-2-2.1-3-4.5-3-7.25s1-5.15 3-7.25Z" />
    </svg>
  )
}
