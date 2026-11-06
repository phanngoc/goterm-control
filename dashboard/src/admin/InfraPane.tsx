import { useCallback, useEffect, useMemo, useState } from 'react'

type Call = (method: string, params?: any) => Promise<any>

// Admin → Infra: what runs on this machine and the few things to do about it.
//
// One screen for what used to take four tools: pm2 (project services), launchd
// (the agents, the tunnel, scheduled jobs), and lsof for whatever nobody
// manages — the dev server left behind, the duplicate static server. Every
// action is checked server-side against a fresh snapshot (internal/infra), so
// this page can only name things that exist.

interface Host {
  load: [number, number, number]
  cpus: number
  mem_total: number
  mem_used: number
  disk_total: number
  disk_free: number
  booted_at?: string
}

interface PM2App {
  name: string
  namespace: string
  status: string
  pid: number
  cpu: number
  mem: number
  started_at: number
  restarts: number
  unstable_restarts: number
  cwd: string
  script: string
  interpreter?: string
  ports?: number[]
  project?: string
  crashloop?: boolean
}

interface Service {
  label: string
  kind: 'bomclaw' | 'other'
  loaded: boolean
  pid?: number
  last_exit: number
  program: string[]
  cwd?: string
  out_log?: string
  err_log?: string
  keep_alive: boolean
  scheduled: boolean
  ports?: number[]
  plist: string
  project?: string
}

interface Listener {
  port: number
  addr: string
  pid: number
  command: string
  cwd?: string
  managed_by?: string
  system?: boolean
  project?: string
}

interface Snapshot {
  at: string
  host: Host
  pm2: PM2App[]
  pm2_bin?: string
  pm2_error?: string
  services: Service[]
  listeners: Listener[]
  errors?: string[]
}

type Kind = 'pm2' | 'launchd' | 'pid'

const gib = (b: number) => `${(b / 2 ** 30).toFixed(1)} GB`
const mib = (b: number) => `${(b / 2 ** 20).toFixed(0)} MB`

function since(ms: number): string {
  if (!ms) return '—'
  const s = Math.max(0, (Date.now() - ms) / 1000)
  if (s < 90) return `${Math.round(s)}s`
  if (s < 5400) return `${Math.round(s / 60)}m`
  if (s < 172800) return `${Math.round(s / 3600)}h`
  return `${Math.round(s / 86400)}d`
}

function Bar({ used, total, warn = 0.85 }: { used: number; total: number; warn?: number }) {
  const r = total > 0 ? used / total : 0
  return (
    <div className="h-1.5 rounded bg-gray-800 overflow-hidden">
      <div className={`h-full ${r > warn ? 'bg-red-400' : r > 0.7 ? 'bg-amber-400' : 'bg-emerald-400'}`} style={{ width: `${Math.min(100, r * 100)}%` }} />
    </div>
  )
}

function Badge({ tone, children, title }: { tone: 'ok' | 'warn' | 'bad' | 'dim'; children: React.ReactNode; title?: string }) {
  const c = {
    ok: 'ring-emerald-500/30 bg-emerald-500/10 text-emerald-300',
    warn: 'ring-amber-500/30 bg-amber-500/10 text-amber-300',
    bad: 'ring-red-500/30 bg-red-500/10 text-red-300',
    dim: 'ring-gray-700 text-gray-500',
  }[tone]
  return <span title={title} className={`text-[10px] px-1.5 py-0.5 rounded ring-1 whitespace-nowrap ${c}`}>{children}</span>
}

function Btn({ onClick, children, danger, disabled, title }: { onClick: () => void; children: React.ReactNode; danger?: boolean; disabled?: boolean; title?: string }) {
  return (
    <button
      onClick={onClick} disabled={disabled} title={title}
      className={`px-1.5 py-0.5 text-[11px] rounded ring-1 disabled:opacity-40 ${danger
        ? 'ring-red-500/30 text-red-300 hover:bg-red-500/10'
        : 'ring-gray-800 text-gray-400 hover:text-sky-300 hover:ring-sky-500/40'}`}
    >
      {children}
    </button>
  )
}

function Ports({ ports }: { ports?: number[] }) {
  if (!ports?.length) return <span className="text-gray-600">—</span>
  return (
    <span className="space-x-1">
      {ports.map(p => (
        <a key={p} href={`http://127.0.0.1:${p}`} target="_blank" rel="noopener" className="font-mono text-sky-300 hover:underline">{p}</a>
      ))}
    </span>
  )
}

function serviceState(s: Service): { tone: 'ok' | 'warn' | 'bad' | 'dim'; text: string } {
  if (!s.loaded) return { tone: 'dim', text: 'chưa nạp' }
  if (s.pid) return { tone: 'ok', text: 'đang chạy' }
  if (s.scheduled) return { tone: s.last_exit ? 'warn' : 'dim', text: s.last_exit ? `theo lịch · lần cuối lỗi ${s.last_exit}` : 'theo lịch' }
  return { tone: s.keep_alive || s.last_exit ? 'bad' : 'dim', text: `dừng${s.last_exit ? ` (exit ${s.last_exit})` : ''}` }
}

function LogsModal({ call, kind, target, onClose }: { call: Call; kind: Kind; target: string; onClose: () => void }) {
  const [logs, setLogs] = useState<Record<string, string> | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [tab, setTab] = useState<'out' | 'err'>('out')
  const load = useCallback(() => {
    call('infra.logs', { kind, target, lines: 300 })
      .then((l: Record<string, string>) => { setLogs(l); setErr(null); if (!l.out && l.err) setTab('err') })
      .catch((e: any) => setErr(String(e?.message ?? e)))
  }, [call, kind, target])
  useEffect(() => { load() }, [load])
  return (
    <div className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-4" onClick={onClose}>
      <div className="w-full max-w-5xl max-h-[85vh] flex flex-col rounded-lg bg-gray-950 ring-1 ring-gray-800" onClick={e => e.stopPropagation()}>
        <div className="flex items-center gap-2 px-3 py-2 border-b border-gray-800">
          <span className="font-mono text-sm text-gray-100">{target}</span>
          {(['out', 'err'] as const).filter(k => logs?.[k] !== undefined).map(k => (
            <button key={k} onClick={() => setTab(k)}
              className={`px-2 py-0.5 text-[11px] rounded ring-1 ${tab === k ? 'ring-sky-500/40 text-sky-300' : 'ring-gray-800 text-gray-500'}`}>
              {k === 'out' ? 'stdout' : 'stderr'}
            </button>
          ))}
          <span className="text-[11px] text-gray-600 font-mono truncate">{logs?.[`${tab}_path`]}</span>
          <div className="ml-auto flex gap-1.5"><Btn onClick={load}>tải lại</Btn><Btn onClick={onClose}>đóng</Btn></div>
        </div>
        {err && <div className="p-3 text-xs text-red-300">{err}</div>}
        <pre className="flex-1 overflow-auto p-3 text-[11px] leading-snug text-gray-300 whitespace-pre-wrap">{logs ? (logs[tab] || '(trống)') : 'Đang tải…'}</pre>
      </div>
    </div>
  )
}

export default function InfraPane({ call }: { call: Call }) {
  const [snap, setSnap] = useState<Snapshot | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [auto, setAuto] = useState(true)
  const [showSystem, setShowSystem] = useState(false)
  const [showIdle, setShowIdle] = useState(false)
  const [logs, setLogs] = useState<{ kind: Kind; target: string } | null>(null)

  const load = useCallback(async () => {
    try {
      setSnap(await call('infra.snapshot'))
      setErr(null)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }, [call])

  useEffect(() => { load() }, [load])
  useEffect(() => {
    if (!auto) return
    const id = setInterval(load, 5000)
    return () => clearInterval(id)
  }, [auto, load])

  const act = useCallback(async (kind: Kind, target: string, verb: string, confirmText?: string) => {
    if (confirmText && !confirm(confirmText)) return
    setBusy(`${kind}:${target}`)
    setNote(null)
    try {
      const r = await call('infra.action', { kind, target, verb })
      setNote(r.pending ? `${r.ran} — đang chạy; trang sẽ mất kết nối vài giây rồi tự nối lại` : `✓ ${r.ran}`)
      setErr(null)
      setTimeout(load, kind === 'launchd' ? 2000 : 600)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(null)
    }
  }, [call, load])

  // pm2 apps grouped by the project they run in (or their pm2 namespace).
  const groups = useMemo(() => {
    const m = new Map<string, PM2App[]>()
    for (const a of snap?.pm2 ?? []) {
      const k = a.project || (a.namespace !== 'default' ? a.namespace : 'không thuộc dự án')
      m.set(k, [...(m.get(k) ?? []), a])
    }
    return [...m.entries()]
  }, [snap])

  const alerts = useMemo(() => {
    if (!snap) return []
    const out: { tone: 'bad' | 'warn'; text: string }[] = []
    for (const a of snap.pm2) {
      if (a.crashloop) out.push({ tone: 'bad', text: `pm2 ${a.name}: crashloop (${a.restarts.toLocaleString()} lần restart)` })
      else if (a.status !== 'online' && a.status !== 'stopped') out.push({ tone: 'warn', text: `pm2 ${a.name}: ${a.status}` })
    }
    for (const s of snap.services) {
      if (s.loaded && !s.pid && !s.scheduled && s.keep_alive) out.push({ tone: 'bad', text: `${s.label}: KeepAlive nhưng không chạy (exit ${s.last_exit})` })
    }
    const strays = snap.listeners.filter(l => !l.managed_by && !l.system)
    if (strays.length) out.push({ tone: 'warn', text: `${strays.length} server không ai quản lý: ${strays.map(l => `:${l.port}${l.project ? ` (${l.project})` : ''}`).join(', ')}` })
    if (snap.pm2_error) out.push({ tone: 'bad', text: `pm2: ${snap.pm2_error}` })
    return out
  }, [snap])

  if (!snap) {
    return <div className="p-4 text-sm text-gray-500">{err ? <span className="text-red-300">{err}</span> : 'Đang đọc máy…'}</div>
  }
  const h = snap.host
  const services = snap.services.filter(s => showIdle || s.kind === 'bomclaw' || s.loaded)
  const listeners = snap.listeners.filter(l => showSystem || !l.system)

  return (
    <div className="h-full overflow-y-auto p-4 space-y-4">
      <div className="flex items-baseline gap-3 flex-wrap">
        <h2 className="text-sm text-gray-200 font-medium">Infra</h2>
        <span className="text-xs text-gray-500">mọi thứ đang chạy trên máy này — pm2 theo dự án, launchd, cổng đang mở</span>
        <div className="ml-auto flex items-center gap-2 text-[11px] text-gray-500">
          <label className="flex items-center gap-1"><input type="checkbox" checked={auto} onChange={e => setAuto(e.target.checked)} /> tự làm mới 5s</label>
          <span>{new Date(snap.at).toLocaleTimeString()}</span>
          <Btn onClick={load}>làm mới</Btn>
        </div>
      </div>

      {/* Host */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <div className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
          <div className="text-[10px] uppercase tracking-wider text-gray-500">Tải CPU (1/5/15 phút)</div>
          <div className="mt-1 text-lg text-gray-100 font-mono">{h.load.map(x => x.toFixed(2)).join(' · ')}</div>
          <div className="mt-1"><Bar used={h.load[0]} total={h.cpus || 1} warn={1} /></div>
          <div className="mt-1 text-[11px] text-gray-500">{h.cpus} nhân</div>
        </div>
        <div className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
          <div className="text-[10px] uppercase tracking-wider text-gray-500">Bộ nhớ</div>
          <div className="mt-1 text-lg text-gray-100 font-mono">{gib(h.mem_used)} <span className="text-xs text-gray-500">/ {gib(h.mem_total)}</span></div>
          <div className="mt-1"><Bar used={h.mem_used} total={h.mem_total} /></div>
        </div>
        <div className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
          <div className="text-[10px] uppercase tracking-wider text-gray-500">Ổ đĩa /</div>
          <div className="mt-1 text-lg text-gray-100 font-mono">{gib(h.disk_free)} <span className="text-xs text-gray-500">trống / {gib(h.disk_total)}</span></div>
          <div className="mt-1"><Bar used={h.disk_total - h.disk_free} total={h.disk_total} warn={0.9} /></div>
        </div>
        <div className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40 p-3">
          <div className="text-[10px] uppercase tracking-wider text-gray-500">Đang chạy</div>
          <div className="mt-1 text-lg text-gray-100 font-mono">
            {snap.pm2.filter(a => a.status === 'online').length}<span className="text-xs text-gray-500"> pm2</span>{' · '}
            {snap.services.filter(s => s.pid).length}<span className="text-xs text-gray-500"> launchd</span>
          </div>
          <div className="mt-1 text-[11px] text-gray-500">máy bật {h.booted_at ? since(Date.parse(h.booted_at)) : '—'}</div>
        </div>
      </div>

      {alerts.length > 0 && (
        <div className="space-y-1">
          {alerts.map(a => (
            <div key={a.text} className={`text-xs rounded px-2 py-1 ring-1 ${a.tone === 'bad' ? 'ring-red-500/30 bg-red-500/10 text-red-300' : 'ring-amber-500/30 bg-amber-500/10 text-amber-300'}`}>
              {a.tone === 'bad' ? '⛔' : '⚠'} {a.text}
            </div>
          ))}
        </div>
      )}
      {err && <div className="text-xs text-red-300 bg-red-500/10 ring-1 ring-red-500/30 rounded p-2 whitespace-pre-wrap">{err}</div>}
      {note && <div className="text-xs text-emerald-300 bg-emerald-500/10 ring-1 ring-emerald-500/30 rounded p-2">{note}</div>}

      {/* pm2 */}
      <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40">
        <header className="flex items-baseline gap-2 px-3 py-2 border-b border-gray-800">
          <span className="text-sm text-gray-100">Dịch vụ dự án (pm2)</span>
          <span className="text-[11px] text-gray-500 font-mono truncate">{snap.pm2_bin}</span>
          <span className="ml-auto text-[11px] text-gray-500">mỗi dự án một file <code>~/.goterm-shared/pm2/&lt;dự án&gt;.config.cjs</code></span>
        </header>
        {groups.length === 0 && <p className="px-3 py-3 text-xs text-gray-600">chưa có app pm2 nào</p>}
        {groups.map(([project, apps]) => (
          <div key={project} className="px-3 py-2 border-b border-gray-800/60 last:border-0">
            <div className="text-[10px] uppercase tracking-wider text-gray-500 mb-1">{project}</div>
            <table className="w-full text-xs">
              <tbody>
                {apps.map(a => {
                  const k = `pm2:${a.name}`
                  const online = a.status === 'online'
                  return (
                    <tr key={a.name} className="border-t border-gray-800/40 first:border-0">
                      <td className="py-1 pr-2 font-mono text-gray-100" title={`${a.interpreter ?? ''} ${a.script}\n${a.cwd}`}>{a.name}</td>
                      <td className="py-1 pr-2">
                        <Badge tone={a.crashloop ? 'bad' : online ? 'ok' : a.status === 'stopped' ? 'dim' : 'warn'}>{a.crashloop ? 'crashloop' : a.status}</Badge>
                      </td>
                      <td className="py-1 pr-2"><Ports ports={a.ports} /></td>
                      <td className="py-1 pr-2 text-gray-400 font-mono whitespace-nowrap">pid {a.pid || '—'}</td>
                      <td className="py-1 pr-2 text-gray-400 font-mono whitespace-nowrap">{a.cpu.toFixed(0)}% · {mib(a.mem)}</td>
                      <td className="py-1 pr-2 text-gray-500 whitespace-nowrap" title="thời gian chạy · số lần restart">{online ? since(a.started_at) : '—'} · ↺{a.restarts.toLocaleString()}</td>
                      <td className="py-1 text-right whitespace-nowrap space-x-1">
                        <Btn disabled={busy === k} onClick={() => act('pm2', a.name, 'restart')}>restart</Btn>
                        {online
                          ? <Btn disabled={busy === k} onClick={() => act('pm2', a.name, 'stop', `Dừng ${a.name}?`)}>stop</Btn>
                          : <Btn disabled={busy === k} onClick={() => act('pm2', a.name, 'start')}>start</Btn>}
                        <Btn onClick={() => setLogs({ kind: 'pm2', target: a.name })}>log</Btn>
                        <Btn danger disabled={busy === k} onClick={() => act('pm2', a.name, 'delete', `Xoá ${a.name} khỏi pm2?\n\nFile ecosystem của dự án vẫn còn — pm2 start nó là có lại.`)}>xoá</Btn>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        ))}
      </section>

      {/* launchd */}
      <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40">
        <header className="flex items-baseline gap-2 px-3 py-2 border-b border-gray-800">
          <span className="text-sm text-gray-100">Hệ thống (launchd)</span>
          <span className="text-[11px] text-gray-500">agent bomclaw, tunnel, job theo lịch</span>
          <label className="ml-auto text-[11px] text-gray-500 flex items-center gap-1">
            <input type="checkbox" checked={showIdle} onChange={e => setShowIdle(e.target.checked)} /> hiện job chưa nạp
          </label>
        </header>
        <table className="w-full text-xs">
          <tbody>
            {services.map(s => {
              const st = serviceState(s)
              const k = `launchd:${s.label}`
              const isAgent = s.kind === 'bomclaw'
              const warnText = isAgent
                ? `${s.label} là một phần của bomclaw. Mọi lượt đang chạy trên nó sẽ bị cắt (và được tiếp tục sau).`
                : ''
              return (
                <tr key={s.label} className="border-t border-gray-800/40">
                  <td className="py-1 px-3 font-mono text-gray-100" title={`${s.program.join(' ')}\n${s.plist}`}>
                    {s.label}
                    {s.project && <span className="ml-1 text-[10px] text-gray-500">#{s.project}</span>}
                  </td>
                  <td className="py-1 pr-2"><Badge tone={st.tone}>{st.text}</Badge></td>
                  <td className="py-1 pr-2"><Ports ports={s.ports} /></td>
                  <td className="py-1 pr-2 text-gray-400 font-mono whitespace-nowrap">pid {s.pid || '—'}</td>
                  <td className="py-1 px-3 text-right whitespace-nowrap space-x-1">
                    {s.loaded && <Btn disabled={busy === k} onClick={() => act('launchd', s.label, 'restart', isAgent ? `Restart ${s.label}?\n\n${warnText}` : undefined)}>restart</Btn>}
                    {s.loaded
                      ? <Btn disabled={busy === k} onClick={() => act('launchd', s.label, 'stop', `Dừng ${s.label}? (launchctl bootout — nó không tự chạy lại cho tới khi bấm start)${isAgent ? `\n\n${warnText}` : ''}`)}>stop</Btn>
                      : <Btn disabled={busy === k} onClick={() => act('launchd', s.label, 'start')}>start</Btn>}
                    {(s.out_log || s.err_log) && <Btn onClick={() => setLogs({ kind: 'launchd', target: s.label })}>log</Btn>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </section>

      {/* ports */}
      <section className="rounded-lg ring-1 ring-gray-800 bg-gray-900/40">
        <header className="flex items-baseline gap-2 px-3 py-2 border-b border-gray-800">
          <span className="text-sm text-gray-100">Cổng đang mở</span>
          <span className="text-[11px] text-gray-500">server không ai quản lý = không ai restart, không ai dừng</span>
          <label className="ml-auto text-[11px] text-gray-500 flex items-center gap-1">
            <input type="checkbox" checked={showSystem} onChange={e => setShowSystem(e.target.checked)} /> hiện của macOS/app
          </label>
        </header>
        <table className="w-full text-xs">
          <tbody>
            {listeners.map(l => (
              <tr key={`${l.pid}:${l.port}`} className="border-t border-gray-800/40">
                <td className="py-1 px-3"><Ports ports={[l.port]} /></td>
                <td className="py-1 pr-2 text-gray-500 font-mono">{l.addr}</td>
                <td className="py-1 pr-2">
                  {l.managed_by
                    ? <Badge tone="ok">{l.managed_by}</Badge>
                    : l.system ? <Badge tone="dim">hệ thống</Badge> : <Badge tone="warn">không ai quản lý</Badge>}
                </td>
                <td className="py-1 pr-2 text-gray-300 font-mono">{l.command} <span className="text-gray-600">{l.pid}</span></td>
                <td className="py-1 pr-2 text-gray-500 font-mono truncate max-w-[28rem]" title={l.cwd}>
                  {l.project && <span className="text-gray-300">#{l.project} </span>}{l.cwd}
                </td>
                <td className="py-1 px-3 text-right">
                  {!l.managed_by && !l.system && (
                    <Btn danger disabled={busy === `pid:${l.pid}`}
                      onClick={() => act('pid', String(l.pid), 'kill', `Dừng ${l.command} (pid ${l.pid}) trên cổng ${l.port}?\n${l.cwd ?? ''}\n\nGửi SIGTERM. Nếu cần chạy lâu dài, đưa nó vào pm2 của dự án thay vì chạy tay.`)}>
                      dừng
                    </Btn>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      {snap.errors?.map(e => <p key={e} className="text-[11px] text-amber-300/80">⚠ {e}</p>)}
      {logs && <LogsModal call={call} kind={logs.kind} target={logs.target} onClose={() => setLogs(null)} />}
    </div>
  )
}
