import { useCallback, useEffect, useRef, useState } from 'react'
import { ago } from './format'

type Call = (method: string, params?: any) => Promise<any>

// A project's two environments, side by side — the Lovable shape: DEV is where
// you build and look (this machine: Preview, the build folder, the local
// SQLite file), PROD is what the public sees, on the provider bomclaw.json
// names — Cloudflare today (a Worker on workers.dev and your domain, with a D1
// database), Fly.io next. The panel only knows the provider's name. Publishing is a button,
// never automatic, and uploading the database is a separate, explicit choice
// because it replaces the live tables.

interface DBStatus { engine: string; name: string; exists: boolean; bytes: number; tables: number; error?: string }
interface EnvStatus {
  channel_id: string
  project_dir?: string
  declared: boolean
  error?: string
  spec?: { static?: string; build?: string; domain?: string; name?: string; spa?: boolean; database?: { engine: string; file: string } }
  dev: {
    preview?: { declared: boolean; state?: string; url?: string }
    build?: { static_dir?: string; built: boolean; built_at?: string; files: number; database?: DBStatus }
  }
  prod: {
    provider?: string; title?: string
    declared: boolean; connected: boolean; error?: string; name?: string; deployed: boolean
    deployed_at?: string; url?: string; workers_dev?: string; domains?: string[]; want_domain?: string
    database?: DBStatus
  }
  deploy?: { running: boolean; started: string; finished?: string; with_db: boolean; dry_run: boolean; error?: string; result?: { url?: string } }
}

const btn = 'px-2.5 py-1 text-xs rounded ring-1 transition-colors disabled:opacity-40'

export default function EnvPanel({ call, channelID, channelName, onClose }: {
  call: Call; channelID: string; channelName: string; onClose: () => void
}) {
  const [st, setSt] = useState<EnvStatus | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [logs, setLogs] = useState('')
  const [withDB, setWithDB] = useState(false)
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

  const running = !!st?.deploy?.running
  const p = st?.prod
  const b = st?.dev.build

  return (
    <div onClick={onClose} className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-6">
      <div onClick={e => e.stopPropagation()}
        className="w-full max-w-4xl max-h-[88vh] flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-gray-800">
          <span className="text-sm font-medium text-gray-200">Môi trường · #{channelName}</span>
          {st?.project_dir && <span className="text-[11px] font-mono text-gray-600 truncate">{st.project_dir}</span>}
          <button onClick={onClose} className="ml-auto text-xs text-gray-500 hover:text-gray-200">đóng</button>
        </div>

        <div className="overflow-y-auto p-4 space-y-4">
          {err && <div className="text-xs text-red-300 whitespace-pre-wrap">{err}</div>}
          {st?.error && <div className="text-xs text-amber-300">{st.error}</div>}

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {/* DEV */}
            <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3 space-y-2">
              <div className="flex items-center gap-2">
                <span className="text-[10px] font-semibold tracking-wider px-1.5 py-0.5 rounded bg-sky-500/15 text-sky-300">DEV</span>
                <span className="text-xs text-gray-400">máy này (local)</span>
              </div>
              <Row label="Preview">
                {st?.dev.preview?.declared
                  ? <a className="text-sky-300 hover:underline" href={`/files/${encodeURIComponent(channelID)}?preview=1`} target="_blank" rel="noreferrer">
                      {st.dev.preview.state || 'đã khai báo'} · mở ▸
                    </a>
                  : <span className="text-gray-500">chưa khai báo (bomclaw preview init)</span>}
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
            <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3 space-y-2">
              <div className="flex items-center gap-2">
                <span className="text-[10px] font-semibold tracking-wider px-1.5 py-0.5 rounded bg-orange-500/15 text-orange-300">PROD</span>
                <span className="text-xs text-gray-400">{p?.title || p?.provider || 'Cloudflare'}{p?.name ? ` · ${p.name}` : ''}</span>
              </div>
              {!st?.declared ? (
                <div className="text-xs text-gray-400 space-y-1">
                  <div>Chưa khai báo PROD. Cho agent chạy:</div>
                  <code className="block font-mono text-[11px] text-gray-300 bg-gray-900 rounded p-2">
                    bomclaw env init {channelID} --static dist --build "npm run build" --domain ten.bomclaw.org
                  </code>
                </div>
              ) : !p?.connected ? (
                <div className="text-xs text-amber-300 space-y-1">
                  <div>{p?.error}</div>
                  <a href="/admin/connections" className="inline-block text-sky-300 hover:underline">Mở trang Kết nối ▸</a>
                </div>
              ) : (
                <>
                  <Row label="Trạng thái">
                    {p.error ? <span className="text-red-300">{p.error}</span>
                      : p.deployed ? <span className="text-emerald-300">● live · <span className="text-gray-500">{p.deployed_at ? ago(p.deployed_at) : ''}</span></span>
                      : <span className="text-gray-500">chưa deploy</span>}
                  </Row>
                  {p.url && <Row label="URL"><a className="text-sky-300 hover:underline break-all" href={p.url} target="_blank" rel="noreferrer">{p.url}</a></Row>}
                  {p.workers_dev && p.workers_dev !== p.url && <Row label={p.provider === 'fly' ? 'fly.dev' : 'workers.dev'}><a className="text-gray-400 hover:underline break-all" href={p.workers_dev} target="_blank" rel="noreferrer">{p.workers_dev}</a></Row>}
                  {p.want_domain && !(p.domains ?? []).includes(p.want_domain) && (
                    <Row label="Domain"><span className="text-amber-300">{p.want_domain} — gắn ở lần deploy tới</span></Row>
                  )}
                  {p.database && <Row label="Database"><DB d={p.database} /></Row>}
                </>
              )}
            </section>
          </div>

          {st?.declared && (
            <div className="flex flex-wrap items-center gap-2">
              <button disabled={running || !p?.connected} onClick={() => deploy(false)}
                className={`${btn} ring-orange-500/40 bg-orange-500/15 text-orange-200 hover:bg-orange-500/25`}>
                {running ? 'Đang deploy…' : 'Deploy lên PROD'}
              </button>
              <button disabled={running} onClick={() => deploy(true)}
                className={`${btn} ring-gray-700 text-gray-300 hover:bg-gray-800`}
                title="Build và in cấu hình wrangler, không gửi gì lên Cloudflare">
                Thử (dry run)
              </button>
              {st.spec?.database && (
                <label className="flex items-center gap-1.5 text-xs text-gray-400">
                  <input type="checkbox" checked={withDB} onChange={e => setWithDB(e.target.checked)} />
                  kèm database (thay thế bảng PROD)
                </label>
              )}
              {st.deploy && !running && (
                <span className={`text-xs ${st.deploy.error ? 'text-red-300' : 'text-emerald-300'}`}>
                  {st.deploy.error ? '✗ lần deploy trước lỗi' : st.deploy.dry_run ? '✓ dry run xong' : '✓ deploy xong'}
                  {st.deploy.finished && <span className="text-gray-500"> · {ago(st.deploy.finished)}</span>}
                </span>
              )}
            </div>
          )}

          {logs && (
            <pre ref={logBox} className="max-h-72 overflow-auto rounded-lg bg-black/60 ring-1 ring-gray-800 p-3 text-[11px] leading-relaxed font-mono text-gray-300 whitespace-pre-wrap">
              {logs}
            </pre>
          )}
        </div>
      </div>
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
