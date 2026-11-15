import { useCallback, useEffect, useRef, useState } from 'react'
import { ago } from './format'
import { CloudIcon, Dot, Facts, devState, host, prodState, toneClass, type DBStatus, type EnvStatus } from './InfraBadge'

type Call = (method: string, params?: any) => Promise<any>

// A project's two environments, side by side — the Lovable shape: DEV is where
// you build and look (this machine: Preview, the build folder, the local
// SQLite file), PROD is what the public sees, on the provider bomclaw.json
// names — Cloudflare today (a Worker on workers.dev and your domain, with a D1
// database), Fly.io next. Publishing is a button, never automatic, and
// uploading the database is a separate, explicit choice because it replaces
// the live tables.
//
// The panel leads with the connection — provider, account, plan, services,
// live or not — because that is the question it is opened to answer.

const btn = 'h-8 px-3 text-xs rounded-lg ring-1 cursor-pointer transition-colors duration-200 disabled:opacity-40 disabled:cursor-not-allowed ' +
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400'

export default function EnvPanel({ call, channelID, channelName, onClose }: {
  call: Call; channelID: string; channelName: string; onClose: () => void
}) {
  const [st, setSt] = useState<EnvStatus | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [logs, setLogs] = useState('')
  const [withDB, setWithDB] = useState(false)
  const [copied, setCopied] = useState(false)
  const logBox = useRef<HTMLPreElement>(null)

  const load = useCallback(async () => {
    try {
      const s: EnvStatus = await call('env.status', { channel_id: channelID })
      setSt(s)
      setErr(null)
      if (s.deploy) {
        const l = await call('env.logs', { channel_id: channelID })
        setLogs(l?.logs ?? '')
      }
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call, channelID])

  useEffect(() => { void load() }, [load])
  // Follow a running deploy closely; otherwise refresh now and then.
  useEffect(() => {
    const t = setInterval(load, st?.deploy?.running ? 1500 : 15000)
    return () => clearInterval(t)
  }, [load, st?.deploy?.running])
  useEffect(() => {
    if (logBox.current) logBox.current.scrollTop = logBox.current.scrollHeight
  }, [logs])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const deploy = async (dryRun: boolean) => {
    if (!dryRun && withDB && !confirm('Tải database local lên sẽ THAY THẾ toàn bộ bảng trên PROD. Tiếp tục?')) return
    try {
      await call('env.deploy', { channel_id: channelID, dry_run: dryRun, with_db: withDB })
      setErr(null)
      await load()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  const initCmd = `bomclaw env init ${channelID} --static dist --build "npm run build" --domain ten.bomclaw.org`
  const copyInit = async () => {
    try { await navigator.clipboard.writeText(initCmd); setCopied(true); setTimeout(() => setCopied(false), 1500) } catch { /* no clipboard */ }
  }

  const running = !!st?.deploy?.running
  const p = st?.prod
  const b = st?.dev.build
  const prod = prodState(st)
  const dev = devState(st)
  const tone = toneClass[prod.tone]
  const provider = p?.title || p?.provider || 'Cloudflare'

  return (
    <div onClick={onClose} className="fixed inset-0 z-50 bg-black/70 backdrop-blur-[2px] flex items-center justify-center p-4 sm:p-6">
      <div
        role="dialog" aria-modal="true" aria-labelledby="env-title"
        onClick={e => e.stopPropagation()}
        className="w-full max-w-4xl max-h-[90vh] flex flex-col rounded-2xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl overflow-hidden"
      >
        <div className="flex items-center gap-3 px-5 h-12 border-b border-gray-800 shrink-0">
          <span id="env-title" className="text-sm font-semibold text-gray-100">Hạ tầng · #{channelName}</span>
          {st?.project_dir && <span className="text-[11px] font-mono text-gray-600 truncate min-w-0">{st.project_dir}</span>}
          <button onClick={onClose} aria-label="Đóng"
            className="ml-auto h-8 w-8 grid place-items-center rounded-lg text-gray-500 cursor-pointer transition-colors duration-200 hover:text-gray-200 hover:bg-gray-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400">
            <svg viewBox="0 0 20 20" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden><path d="m5 5 10 10M15 5 5 15" strokeLinecap="round" /></svg>
          </button>
        </div>

        <div className="overflow-y-auto p-5 space-y-4">
          {err && <div role="alert" className="text-xs text-red-300 whitespace-pre-wrap rounded-lg bg-red-500/10 ring-1 ring-red-500/30 p-2.5">{err}</div>}
          {st?.error && <div className="text-xs text-amber-300 rounded-lg bg-amber-500/10 ring-1 ring-amber-500/30 p-2.5">{st.error}</div>}

          {/* The connection: provider, live or not, account, plan, services. */}
          <section className={`relative rounded-xl ring-1 ${tone.ring} overflow-hidden`}>
            <div className={`absolute inset-0 ${tone.soft} pointer-events-none`} aria-hidden />
            <div className="relative p-4 grid gap-4 md:grid-cols-[1fr_auto] md:items-start">
              <div className="space-y-3 min-w-0">
                <div className="flex items-center gap-3">
                  <span className={`h-10 w-10 shrink-0 grid place-items-center rounded-xl bg-gray-900 ring-1 ${tone.ring}`}>
                    <CloudIcon className={`h-5 w-5 ${tone.text}`} />
                  </span>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-base font-semibold text-gray-100">{provider}</span>
                      <span className={`inline-flex items-center gap-1.5 h-5 px-2 rounded-full text-[11px] ${tone.text} bg-gray-900/70 ring-1 ${tone.ring}`}>
                        <Dot tone={prod.tone} size="h-1.5 w-1.5" />{prod.tone === 'live' ? 'PROD live' : prod.label}
                      </span>
                    </div>
                    <div className="text-[11px] text-gray-500 mt-0.5">
                      {p?.connected ? 'Hạ tầng đã kết nối' : 'Hạ tầng chưa kết nối'}
                      {p?.name ? <> · <span className="font-mono">{p.name}</span></> : null}
                    </div>
                  </div>
                </div>
                {st && <div className="text-xs"><Facts st={st} /></div>}
              </div>

              <div className="flex flex-col gap-2 md:items-end">
                {!p?.connected ? (
                  <a href="/admin/connections"
                    className={`${btn} inline-flex items-center ring-amber-500/40 bg-amber-500/15 text-amber-200 hover:bg-amber-500/25`}>
                    Kết nối {provider} ▸
                  </a>
                ) : st?.declared ? (
                  <>
                    <div className="flex gap-2">
                      <button disabled={running} onClick={() => deploy(true)}
                        className={`${btn} ring-gray-700 text-gray-300 hover:bg-gray-800`}
                        title="Build và in cấu hình, không gửi gì lên PROD">
                        Thử (dry run)
                      </button>
                      <button disabled={running} onClick={() => deploy(false)}
                        className={`${btn} ring-emerald-500/50 bg-emerald-500/15 text-emerald-200 hover:bg-emerald-500/25 font-medium`}>
                        {running ? 'Đang deploy…' : 'Deploy lên PROD'}
                      </button>
                    </div>
                    {st.spec?.database && (
                      <label className="flex items-center gap-1.5 text-xs text-gray-400 cursor-pointer">
                        <input type="checkbox" checked={withDB} onChange={e => setWithDB(e.target.checked)} className="accent-orange-400" />
                        kèm database <span className="text-orange-300">(thay thế bảng PROD)</span>
                      </label>
                    )}
                    {st.deploy && !running && (
                      <span className={`text-xs ${st.deploy.error ? 'text-red-300' : 'text-emerald-300'}`}>
                        {st.deploy.error ? '✗ lần deploy trước lỗi' : st.deploy.dry_run ? '✓ dry run xong' : '✓ deploy xong'}
                        {st.deploy.finished && <span className="text-gray-500"> · {ago(st.deploy.finished)}</span>}
                      </span>
                    )}
                  </>
                ) : null}
              </div>
            </div>
            {!st?.declared && p?.connected && (
              <div className="relative border-t border-gray-800/80 px-4 py-3 text-xs text-gray-400 space-y-2">
                <div>Dự án chưa khai báo PROD. Nhờ agent chạy (sửa domain cho đúng):</div>
                <div className="flex items-stretch gap-2">
                  <code className="flex-1 min-w-0 font-mono text-[11px] text-gray-200 bg-gray-900 rounded-lg px-3 py-2 overflow-x-auto whitespace-nowrap">{initCmd}</code>
                  <button onClick={copyInit} className={`${btn} ring-gray-700 text-gray-300 hover:bg-gray-800 shrink-0`}>
                    {copied ? 'đã chép' : 'Chép'}
                  </button>
                </div>
              </div>
            )}
          </section>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {/* DEV */}
            <section className="rounded-xl ring-1 ring-gray-800 bg-gray-900/40 p-4 space-y-2.5">
              <EnvHead tag="DEV" tagCls="bg-sky-500/15 text-sky-300" title="máy này (local)" tone={dev.tone} state={dev.label} />
              <Row label="Preview">
                {st?.dev.preview?.declared
                  ? <a className="text-sky-300 hover:underline cursor-pointer" href={`/files/${encodeURIComponent(channelID)}?preview=1`} target="_blank" rel="noreferrer">
                      mở Preview ▸
                    </a>
                  : <span className="text-gray-500">chưa khai báo (<code className="font-mono">bomclaw preview init</code>)</span>}
              </Row>
              {b?.static_dir && (
                <Row label="Build">
                  {b.built
                    ? <span>{b.files} file · <span className="text-gray-500">{b.built_at ? ago(b.built_at) : ''}</span></span>
                    : <span className="text-amber-300">trống — chưa build</span>}
                  <div className="text-[11px] font-mono text-gray-600 truncate">{b.static_dir}</div>
                </Row>
              )}
              {b?.database && <Row label="Database"><DB d={b.database} /></Row>}
            </section>

            {/* PROD */}
            <section className="rounded-xl ring-1 ring-gray-800 bg-gray-900/40 p-4 space-y-2.5">
              <EnvHead tag="PROD" tagCls="bg-orange-500/15 text-orange-300" title={provider} tone={prod.tone} state={prod.label} />
              {!st?.declared ? (
                <div className="text-xs text-gray-500">Chưa khai báo — xem lệnh ở trên.</div>
              ) : !p?.connected ? (
                <div className="text-xs text-amber-300">{p?.error}</div>
              ) : (
                <>
                  {p.error && <Row label="Lỗi"><span className="text-red-300">{p.error}</span></Row>}
                  {p.url && <Row label="URL"><a className="text-sky-300 hover:underline break-all font-mono" href={p.url} target="_blank" rel="noreferrer">{host(p.url)}</a></Row>}
                  {p.workers_dev && p.workers_dev !== p.url && (
                    <Row label={p.provider === 'fly' ? 'fly.dev' : 'workers.dev'}>
                      <a className="text-gray-400 hover:underline break-all font-mono" href={p.workers_dev} target="_blank" rel="noreferrer">{host(p.workers_dev)}</a>
                    </Row>
                  )}
                  {p.want_domain && !(p.domains ?? []).includes(p.want_domain) && (
                    <Row label="Domain"><span className="text-amber-300">{p.want_domain} — gắn ở lần deploy tới</span></Row>
                  )}
                  {p.database && <Row label="Database"><DB d={p.database} /></Row>}
                  {!p.deployed && !p.error && <Row label="Trạng thái"><span className="text-gray-500">chưa deploy lần nào</span></Row>}
                </>
              )}
            </section>
          </div>

          {logs && (
            <pre ref={logBox} className="max-h-72 overflow-auto rounded-xl bg-black/60 ring-1 ring-gray-800 p-3 text-[11px] leading-relaxed font-mono text-gray-300 whitespace-pre-wrap">
              {logs}
            </pre>
          )}
        </div>
      </div>
    </div>
  )
}

function EnvHead({ tag, tagCls, title, tone, state }: { tag: string; tagCls: string; title: string; tone: Parameters<typeof Dot>[0]['tone']; state: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className={`text-[10px] font-semibold tracking-wider px-1.5 py-0.5 rounded ${tagCls}`}>{tag}</span>
      <span className="text-xs text-gray-300">{title}</span>
      <span className={`ml-auto inline-flex items-center gap-1.5 text-[11px] ${toneClass[tone].text}`}>
        <Dot tone={tone} size="h-1.5 w-1.5" />{state}
      </span>
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[84px_1fr] gap-2 text-xs">
      <span className="text-gray-500">{label}</span>
      <div className="text-gray-200 min-w-0">{children}</div>
    </div>
  )
}

function DB({ d }: { d: DBStatus }) {
  if (d.error) return <span className="text-red-300">{d.error}</span>
  if (!d.exists) return <span className="text-gray-500">{d.engine === 'd1' ? `D1 ${d.name} — chưa tạo` : 'không có file'}</span>
  return (
    <span>
      <span className="uppercase text-[10px] text-gray-500 mr-1">{d.engine}</span>
      {d.tables} bảng · {(d.bytes / 1024).toFixed(0)} KB
      <div className="text-[11px] font-mono text-gray-600 truncate">{d.name}</div>
    </span>
  )
}
