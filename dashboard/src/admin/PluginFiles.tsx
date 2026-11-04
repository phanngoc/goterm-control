import { useEffect, useMemo, useState } from 'react'
import Editor from '@monaco-editor/react'
import './monacoSetup'
import MessageMarkdown from '../components/MessageMarkdown'

type Call = (method: string, params?: any) => Promise<any>

interface Entry {
  path: string
  name: string
  dir: boolean
  bytes?: number
  mode?: string
  link?: string
}

interface FileView {
  path: string
  body: string
  truncated: boolean
  binary: boolean
}

// PluginFiles is a read-only look inside one plugin: what it is made of, and
// what each file says — the manifest, the scripts a hook would run before
// every tool call, the skill text the agent will read. Read-only on purpose:
// this is where someone decides whether to trust a plugin, and editing
// someone else's code from here is how you end up trusting what you did not
// read.
//
// Loaded lazily (Monaco is heavy): the Plugins pane only pays for it when a
// plugin is opened.
export default function PluginFiles({ call, agent, name, onClose }: {
  call: Call; agent: string; name: string; onClose: () => void
}) {
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [dir, setDir] = useState('')
  const [truncated, setTruncated] = useState(false)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [open, setOpen] = useState<string>('')
  const [file, setFile] = useState<FileView | null>(null)
  const [rendered, setRendered] = useState(true)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    call('plugins.files', { agent_id: agent, name })
      .then((r: any) => {
        const list: Entry[] = r?.entries ?? []
        setEntries(list)
        setDir(r?.dir ?? '')
        setTruncated(!!r?.truncated)
        // Open what explains the plugin first: its manifest, else a SKILL.md.
        const first = list.find(e => e.path === 'plugin.yaml')
          ?? list.find(e => e.name === 'SKILL.md')
          ?? list.find(e => !e.dir)
        if (first) setOpen(first.path)
      })
      .catch((e: any) => setErr(String(e?.message ?? e)))
  }, [call, agent, name])

  useEffect(() => {
    if (!open) return
    setFile(null)
    call('plugins.files', { agent_id: agent, name, path: open })
      .then((r: any) => { setFile(r); setErr(null) })
      .catch((e: any) => setErr(String(e?.message ?? e)))
  }, [call, agent, name, open])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // A path is hidden when any folder above it is collapsed.
  const visible = useMemo(() => (entries ?? []).filter(e => {
    const parts = e.path.split('/')
    for (let i = 1; i < parts.length; i++) {
      if (collapsed.has(parts.slice(0, i).join('/'))) return false
    }
    return true
  }), [entries, collapsed])

  const toggle = (path: string) => setCollapsed(c => {
    const next = new Set(c)
    if (next.has(path)) next.delete(path); else next.add(path)
    return next
  })

  const isMarkdown = open.toLowerCase().endsWith('.md')
  const fileCount = (entries ?? []).filter(e => !e.dir).length

  return (
    <div onClick={onClose} className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-2 sm:p-6">
      <div onClick={e => e.stopPropagation()}
        className="w-full max-w-6xl h-[90vh] flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl overflow-hidden">
        <header className="flex items-center gap-3 px-4 py-2.5 border-b border-gray-800">
          <span className="text-sm text-gray-200 font-medium">{name}</span>
          <span className="text-[11px] text-gray-600 font-mono">{agent}</span>
          <span className="hidden md:inline text-[10px] text-gray-700 font-mono truncate">{dir}</span>
          <span className="text-[10px] px-1.5 py-0.5 rounded ring-1 ring-gray-700 text-gray-500">chỉ đọc</span>
          <button onClick={onClose} className="ml-auto text-xs text-gray-500 hover:text-gray-300">đóng</button>
        </header>

        {err && <div className="px-4 py-2 text-xs text-red-300 bg-red-500/10 border-b border-red-500/20">{err}</div>}

        <div className="flex-1 min-h-0 flex flex-col md:flex-row">
          <nav className="md:w-72 max-h-48 md:max-h-none overflow-y-auto border-b md:border-b-0 md:border-r border-gray-800 py-2">
            {entries === null ? (
              <div className="px-3 text-xs text-gray-500">Đang tải…</div>
            ) : entries.length === 0 ? (
              <div className="px-3 text-xs text-gray-600">thư mục trống</div>
            ) : (
              <>
                <div className="px-3 pb-1 text-[10px] uppercase tracking-wider text-gray-600">
                  {fileCount} file{truncated && ' · danh sách đã bị cắt'}
                </div>
                {visible.map(e => {
                  const depth = e.path.split('/').length - 1
                  const active = e.path === open
                  return (
                    <button
                      key={e.path}
                      onClick={() => (e.dir ? toggle(e.path) : !e.link && setOpen(e.path))}
                      title={e.link ? `symlink → ${e.link} (không mở, vì có thể trỏ ra ngoài plugin)` : e.path}
                      style={{ paddingLeft: 12 + depth * 14 }}
                      className={`w-full flex items-center gap-1.5 pr-3 py-0.5 text-left text-xs font-mono ${
                        active ? 'bg-sky-500/15 text-sky-200' : 'text-gray-300 hover:bg-gray-800/60'
                      } ${e.link ? 'opacity-60 cursor-default' : ''}`}
                    >
                      <span className="w-3 text-gray-600">{e.dir ? (collapsed.has(e.path) ? '▸' : '▾') : ''}</span>
                      <span className={e.dir ? 'text-gray-200' : ''}>{e.name}{e.dir && '/'}</span>
                      {e.mode === 'x' && <span className="text-[9px] text-emerald-400" title="Lệnh chạy được">x</span>}
                      {e.link && <span className="text-[9px] text-amber-300">→</span>}
                      {!e.dir && e.bytes !== undefined && (
                        <span className="ml-auto text-[10px] text-gray-600">{fmtBytes(e.bytes)}</span>
                      )}
                    </button>
                  )
                })}
              </>
            )}
          </nav>

          <section className="flex-1 min-w-0 min-h-0 flex flex-col">
            {open && (
              <div className="flex items-center gap-2 px-4 py-1.5 border-b border-gray-800">
                <span className="text-xs text-gray-400 font-mono truncate">{open}</span>
                {isMarkdown && file && !file.binary && (
                  <button onClick={() => setRendered(r => !r)} className="ml-auto text-[11px] text-sky-300 hover:underline">
                    {rendered ? 'xem mã nguồn' : 'xem bản hiển thị'}
                  </button>
                )}
              </div>
            )}
            {file?.truncated && (
              <div className="px-4 py-1 text-[11px] text-amber-300 bg-amber-500/10">
                File lớn — chỉ hiện 512 KB đầu.
              </div>
            )}
            <div className="flex-1 min-h-0">
              {!open ? (
                <div className="p-6 text-sm text-gray-500">Chọn một file bên trái.</div>
              ) : file === null ? (
                <div className="p-6 text-sm text-gray-500">Đang tải…</div>
              ) : file.binary ? (
                <div className="p-6 text-sm text-gray-500">File nhị phân — không hiển thị được ở đây.</div>
              ) : isMarkdown && rendered ? (
                <div className="h-full overflow-y-auto px-6 py-4"><MessageMarkdown wide>{file.body}</MessageMarkdown></div>
              ) : (
                <Editor
                  path={`/plugins/${agent}/${name}/${open}`}
                  value={file.body}
                  theme="vs-dark"
                  options={{
                    readOnly: true,
                    domReadOnly: true,
                    fontSize: 13,
                    minimap: { enabled: false },
                    automaticLayout: true,
                    scrollBeyondLastLine: false,
                    wordWrap: isMarkdown ? 'on' : 'off',
                    renderWhitespace: 'selection',
                  }}
                />
              )}
            </div>
          </section>
        </div>
      </div>
    </div>
  )
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
