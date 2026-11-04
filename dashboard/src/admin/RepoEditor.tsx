import { useCallback, useEffect, useRef, useState } from 'react'

type Call = (method: string, params?: any) => Promise<any>

export interface ChannelRepo {
  id: string
  channel_id: string
  provider: string
  repo: string
  url: string
  role: 'primary' | 'secondary'
  path?: string
  branch?: string
  label?: string
  added_by: string
  created_at: string
}

const field = 'px-2 py-1.5 text-sm bg-gray-900 rounded ring-1 ring-gray-800 text-gray-200 outline-none focus:ring-gray-600'

export function GitHubMark({ className = 'h-3.5 w-3.5' }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" className={className} fill="currentColor" aria-hidden>
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  )
}

// RepoEditor is a room's GitHub connection: the repos its code lives in, one
// of them primary — "the repo" when nobody says which. Linking here is a
// record every agent in the room is told; cloning a repo into the project
// folder is `bomclaw ch repo add --clone` (or ask an agent), since that runs
// where the code goes.
export default function RepoEditor({ call, channelID, channelName, onClose, onChanged }: {
  call: Call; channelID: string; channelName: string
  onClose: () => void; onChanged?: (repos: ChannelRepo[]) => void
}) {
  const [repos, setRepos] = useState<ChannelRepo[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [repo, setRepo] = useState('')
  const [path, setPath] = useState('')
  const [label, setLabel] = useState('')
  const [primary, setPrimary] = useState(false)
  // Held in a ref: the parent passes a fresh arrow each render, and putting it
  // in apply's deps would re-run the load effect forever.
  const changed = useRef(onChanged)
  changed.current = onChanged

  const apply = useCallback(async (method: string, params: any = {}) => {
    setBusy(true)
    try {
      const r = await call(method, { channel_id: channelID, ...params })
      const list: ChannelRepo[] = r?.repos ?? []
      setRepos(list)
      changed.current?.(list)
      setErr(null)
      return true
    } catch (e: any) {
      setErr(String(e?.message ?? e))
      return false
    } finally {
      setBusy(false)
    }
  }, [call, channelID])

  useEffect(() => { apply('channels.repos') }, [apply])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const add = async () => {
    if (!repo.trim()) return
    if (await apply('channels.repo_add', { repo: repo.trim(), path, label, primary })) {
      setRepo(''); setPath(''); setLabel(''); setPrimary(false)
    }
  }

  return (
    <div onClick={onClose} className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-4 sm:p-6">
      <div onClick={e => e.stopPropagation()} className="w-full max-w-2xl rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl">
        <header className="flex items-center gap-2 border-b border-gray-800 px-4 py-3">
          <GitHubMark className="h-4 w-4 text-gray-300" />
          <h3 className="text-sm font-medium text-gray-100">GitHub · #{channelName}</h3>
          <button onClick={onClose} className="ml-auto text-xs text-gray-500 hover:text-gray-300">đóng</button>
        </header>

        <div className="p-4 space-y-4">
          {err && <div className="rounded bg-red-950/60 px-3 py-2 text-sm text-red-200 break-words">{err}</div>}

          {repos.length === 0 ? (
            <p className="text-sm text-gray-400">
              Phòng này chưa link repo nào. Repo đầu tiên link vào sẽ là <b className="text-gray-200">repo chính</b>: khi ai đó nói "repo của dự án" mà không nói rõ repo nào, đó là repo này.
            </p>
          ) : (
            <ul className="divide-y divide-gray-800 rounded-lg ring-1 ring-gray-800">
              {repos.map(r => (
                <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
                  <a href={r.url} target="_blank" rel="noopener noreferrer" className="flex min-w-0 items-center gap-1.5 font-mono text-sm text-sky-300 hover:underline">
                    <GitHubMark className="h-3.5 w-3.5 shrink-0 text-gray-400" />
                    <span className="truncate">{r.repo}</span>
                  </a>
                  {r.role === 'primary'
                    ? <span className="rounded bg-emerald-500/15 px-1.5 text-[11px] text-emerald-300 ring-1 ring-emerald-500/30">chính</span>
                    : <span className="rounded bg-gray-800 px-1.5 text-[11px] text-gray-400">phụ</span>}
                  {r.branch && <span className="font-mono text-[11px] text-gray-500">⎇ {r.branch}</span>}
                  {r.label && <span className="text-xs text-gray-400">{r.label}</span>}
                  <span className="ml-auto flex items-center gap-3 text-xs">
                    {r.role !== 'primary' && (
                      <button disabled={busy} onClick={() => apply('channels.repo_primary', { repo: r.id })} className="text-gray-400 hover:text-emerald-300 disabled:opacity-40">đặt làm chính</button>
                    )}
                    <button disabled={busy} onClick={() => apply('channels.repo_remove', { repo: r.id })} className="text-gray-500 hover:text-red-300 disabled:opacity-40" title="Chỉ bỏ link; repo trên GitHub và bản checkout giữ nguyên">bỏ link</button>
                  </span>
                  {r.path && <div className="w-full font-mono text-[11px] text-gray-500 truncate" title={r.path}>checkout: {r.path}</div>}
                </li>
              ))}
            </ul>
          )}

          <div className="space-y-2 rounded-lg bg-gray-900/40 p-3 ring-1 ring-gray-800">
            <div className="text-[11px] uppercase tracking-wide text-gray-500">Link repo</div>
            <div className="flex flex-col gap-2 sm:flex-row">
              <input value={repo} onChange={e => setRepo(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') add() }}
                placeholder="owner/name hoặc https://github.com/…" className={`${field} flex-1 font-mono`} />
              <input value={path} onChange={e => setPath(e.target.value)} placeholder="checkout (tuỳ chọn): web, ~/…" className={`${field} sm:w-48 font-mono`} />
            </div>
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <input value={label} onChange={e => setLabel(e.target.value)} placeholder="ghi chú (tuỳ chọn): game Rumba, bản production…" className={`${field} flex-1`} />
              <label className="flex items-center gap-1.5 text-xs text-gray-300">
                <input type="checkbox" checked={primary} onChange={e => setPrimary(e.target.checked)} /> repo chính
              </label>
              <button disabled={busy || !repo.trim()} onClick={add} className="rounded bg-gray-100 px-3 py-1.5 text-sm font-medium text-gray-900 hover:bg-white disabled:opacity-40">Link</button>
            </div>
          </div>

          <p className="text-xs text-gray-500">
            Mọi agent làm việc trong phòng đều được báo danh sách này. Muốn clone vào thư mục dự án hoặc tạo repo mới:
            {' '}<code className="text-gray-400">bomclaw ch repo add {channelID} owner/name --clone</code>,
            {' '}<code className="text-gray-400">bomclaw ch repo create {channelID} &lt;name&gt;</code>, hoặc nhắn agent.
          </p>
        </div>
      </div>
    </div>
  )
}
