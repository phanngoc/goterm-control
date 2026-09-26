import { useCallback, useEffect, useRef, useState } from 'react'

type Call = (method: string, params?: any) => Promise<any>

interface Status {
  channel_id: string
  declared: boolean
  kind?: 'server' | 'static'
  state: 'none' | 'stopped' | 'starting' | 'running' | 'exited'
  url?: string
  port?: number
  command?: string
  started_at?: string
  exit_code?: number
  error?: string
}

const errText = (e: any) => String(e?.message ?? e)

// PreviewPanel is the running project next to its code, the way Lovable shows
// the app beside the chat: open it and the project's dev server starts (from
// bomclaw.json — the `preview` skill is what teaches agents to write one), the
// page loads in the frame, and a dev server with hot reload updates as files
// change.
export default function PreviewPanel({ call, channelID, onClose }: {
  call: Call; channelID: string; onClose: () => void
}) {
  const [st, setSt] = useState<Status | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [sub, setSub] = useState('') // path inside the app, after its base
  const [frameKey, setFrameKey] = useState(0)
  const [showLogs, setShowLogs] = useState(false)
  const [logs, setLogs] = useState('')
  const [mobile, setMobile] = useState(false)
  const autoStarted = useRef(false)
  const logRef = useRef<HTMLPreElement>(null)

  const refresh = useCallback(async () => {
    try {
      const s: Status = await call('preview.status', { channel_id: channelID })
      setSt(s)
      return s
    } catch (e) {
      setErr(errText(e))
      return null
    }
  }, [call, channelID])

  const start = useCallback(async () => {
    setBusy(true)
    setErr(null)
    try {
      setSt(await call('preview.start', { channel_id: channelID }))
      setFrameKey(k => k + 1)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }, [call, channelID])

  const stop = async () => {
    setBusy(true)
    try { setSt(await call('preview.stop', { channel_id: channelID })) } catch (e) { setErr(errText(e)) }
    finally { setBusy(false) }
  }

  // Opening the panel is asking to see the project: a declared server that is
  // not running is started, once, without a second click.
  useEffect(() => {
    refresh().then(s => {
      if (!autoStarted.current && s?.kind === 'server' && (s.state === 'stopped' || s.state === 'exited')) {
        autoStarted.current = true
        start()
      }
    })
  }, [refresh, start])

  // Poll quickly while it comes up, slowly after; the frame reloads the
  // moment the server first answers.
  const prevState = useRef<string>('')
  useEffect(() => {
    const t = setInterval(refresh, st?.state === 'starting' ? 1000 : 5000)
    return () => clearInterval(t)
  }, [refresh, st?.state])
  useEffect(() => {
    if (st?.state === 'running' && prevState.current === 'starting') setFrameKey(k => k + 1)
    prevState.current = st?.state ?? ''
    // A server that died or never came up: show why without being asked.
    if (st?.kind === 'server' && (st.state === 'exited' || (st.state === 'starting' && st.error))) setShowLogs(true)
  }, [st?.state, st?.kind, st?.error])

  useEffect(() => {
    if (!showLogs || st?.kind !== 'server') return
    const load = () => call('preview.logs', { channel_id: channelID }).then((r: any) => setLogs(r?.logs ?? '')).catch(() => {})
    load()
    const t = setInterval(load, 1500)
    return () => clearInterval(t)
  }, [showLogs, st?.kind, call, channelID])
  useEffect(() => {
    const el = logRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [logs])

  const src = st?.url ? st.url + sub.replace(/^\/+/, '') : ''
  const dot = st?.state === 'running' ? 'bg-emerald-400' : st?.state === 'starting' ? 'bg-amber-400 animate-pulse' : st?.state === 'exited' ? 'bg-red-400' : 'bg-gray-500'
  const label = !st ? '…'
    : !st.declared ? 'chưa khai báo'
      : st.kind === 'static' ? 'static'
        : st.state === 'running' ? `đang chạy · :${st.port}`
          : st.state === 'starting' ? 'đang khởi động…'
            : st.state === 'exited' ? `đã dừng (mã ${st.exit_code ?? '?'})`
              : 'chưa chạy'

  return (
    <div className="h-full flex flex-col bg-[#1e1e1e] border-l border-black/60 min-w-0">
      <div className="flex items-center gap-2 h-9 px-2 bg-[#181818] shrink-0 text-[12px]">
        <span className="text-[11px] uppercase tracking-wide text-gray-500">Preview</span>
        <span className={`w-2 h-2 rounded-full shrink-0 ${dot}`} />
        <span className="text-gray-400 whitespace-nowrap">{label}</span>
        {st?.url && (
          <div className="flex-1 min-w-0 flex items-center rounded bg-[#2d2d2d] px-2 h-6 font-mono text-[11px]">
            <span className="text-gray-500 truncate shrink max-w-[45%]">{st.url}</span>
            <input
              value={sub} onChange={e => setSub(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') setFrameKey(k => k + 1) }}
              placeholder=""
              className="flex-1 min-w-[3rem] bg-transparent text-gray-200 outline-none"
            />
          </div>
        )}
        <span className="ml-auto flex items-center gap-2 shrink-0">
          {st?.kind === 'server' && (st.state === 'running' || st.state === 'starting'
            ? <>
                <button onClick={start} disabled={busy} title="Khởi động lại dev server" className="text-gray-400 hover:text-white disabled:opacity-40">chạy lại</button>
                <button onClick={stop} disabled={busy} title="Dừng dev server" className="text-gray-400 hover:text-red-300 disabled:opacity-40">dừng</button>
              </>
            : <button onClick={start} disabled={busy} className="text-sky-300 hover:underline disabled:opacity-40">chạy</button>)}
          {st?.url && <button onClick={() => setFrameKey(k => k + 1)} title="Tải lại trang" className="text-gray-400 hover:text-white">⟳</button>}
          {st?.url && (
            <button onClick={() => setMobile(m => !m)} title="Xem cỡ điện thoại / máy tính" className={mobile ? 'text-sky-300' : 'text-gray-400 hover:text-white'}>
              {mobile ? '📱' : '🖥'}
            </button>
          )}
          {st?.kind === 'server' && (
            <button onClick={() => setShowLogs(v => !v)} className={showLogs ? 'text-sky-300' : 'text-gray-400 hover:text-white'}>logs</button>
          )}
          {src && <a href={src} target="_blank" rel="noopener noreferrer" title="Mở ở tab riêng" className="text-gray-400 hover:text-sky-300">↗</a>}
          <button onClick={onClose} title="Đóng preview" className="text-gray-400 hover:text-white">×</button>
        </span>
      </div>

      {(err || st?.error) && (
        <div className="px-3 py-1.5 text-xs bg-red-950/70 text-red-200 shrink-0 break-words">{err || st?.error}</div>
      )}

      <div className="flex-1 min-h-0 flex flex-col">
        {st && !st.declared ? (
          <NotDeclared channelID={channelID} />
        ) : src && (st?.kind === 'static' || st?.state === 'running') ? (
          <div className={`flex-1 min-h-0 flex justify-center ${mobile ? 'bg-[#111] py-3' : ''}`}>
            <iframe
              key={frameKey} src={src} title="preview"
              className={`h-full bg-white ${mobile ? 'w-[390px] max-w-full rounded-xl ring-1 ring-gray-700' : 'w-full'}`}
            />
          </div>
        ) : (
          <div className="flex-1 flex items-center justify-center text-sm text-gray-500 px-6 text-center">
            {st?.state === 'starting' ? `Đang chạy ${st.command ?? ''}…` : st?.state === 'exited' ? 'Dev server đã dừng — xem logs.' : busy ? 'Đang khởi động…' : 'Chưa chạy.'}
          </div>
        )}
        {showLogs && st?.kind === 'server' && (
          <pre ref={logRef} className="h-40 shrink-0 overflow-auto border-t border-black/60 bg-[#141414] px-3 py-2 text-[11px] leading-snug text-gray-300 whitespace-pre-wrap">
            {logs || '(chưa có output)'}
          </pre>
        )}
      </div>
    </div>
  )
}

function NotDeclared({ channelID }: { channelID: string }) {
  return (
    <div className="flex-1 overflow-y-auto px-5 py-4 text-sm text-gray-400 space-y-3">
      <p className="text-gray-200">Dự án chưa khai báo cách chạy preview.</p>
      <p>
        Thêm <code className="text-sky-300">bomclaw.json</code> ở thư mục gốc dự án — hoặc nhắn agent trong phòng:
        {' '}<em>“làm preview cho dự án theo skill preview”</em>.
      </p>
      <pre className="rounded bg-[#141414] p-3 text-[12px] text-gray-300 overflow-x-auto">{`{
  "preview": {
    "command": "npm run dev -- --host $HOST --port $PORT --base $BASE_PATH",
    "setup": "npm install"
  }
}`}</pre>
      <p>Chỉ là HTML/JS tĩnh? <code className="text-sky-300">{`{"preview": {"static": "board"}}`}</code></p>
      <p className="text-[12px] text-gray-500">
        Dashboard đặt <code>$PORT</code>, <code>$HOST</code>, <code>$BASE_PATH</code> (= /preview/{channelID}/) khi chạy lệnh.
      </p>
    </div>
  )
}
