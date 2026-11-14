import { useCallback, useEffect, useMemo, useState } from 'react'
import MessageMarkdown from '../components/MessageMarkdown'

// WikiPanel is a room's wiki: the documents people and agents keep for it —
// markdown, HTML, plain text — listed on the left, read or edited on the
// right, and hosted at /wiki/<room>/<page> so a page can be linked to.
//
// Pages are files (a project's wiki/ folder, or ~/goterm-shared/wiki/<room>),
// so an agent writes them with `bomclaw wiki put` or by saving a file, and a
// person edits the same file here. A save carries the mtime the page was
// opened at; if an agent changed it meanwhile the save is refused, not merged.

type Call = (method: string, params?: any) => Promise<any>

interface Page { path: string; title: string; format: string; bytes: number; mtime: string }

const PAGE_EXT = /\.(md|markdown|html?|txt|json|csv|svg)$/i

export function looksLikeHTML(s: string): boolean {
  return /^\s*(<!doctype html|<html[\s>]|<head[\s>]|<body[\s>])/i.test(s)
}

/** A page shown as a page, sandboxed: its scripts run, but it is not this
 *  origin, so it cannot act as the person signed in to the dashboard. */
export function HtmlFrame({ html, src, title }: { html?: string; src?: string; title: string }) {
  return (
    <iframe
      title={title}
      {...(src ? { src } : { srcDoc: html ?? '' })}
      sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals"
      className="h-full w-full border-0 bg-white"
    />
  )
}

function formatOf(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase() ?? ''
  if (ext === 'md' || ext === 'markdown') return 'md'
  if (ext === 'html' || ext === 'htm') return 'html'
  return ext
}

function ago(iso: string): string {
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (s < 60) return 'vừa xong'
  if (s < 3600) return `${Math.floor(s / 60)} phút`
  if (s < 86400) return `${Math.floor(s / 3600)} giờ`
  return `${Math.floor(s / 86400)} ngày`
}

/** A file name from a title: ascii, dashes, kept short. */
export function slugify(s: string): string {
  return s.normalize('NFD').replace(/[̀-ͯ]/g, '').replace(/đ/g, 'd').replace(/Đ/g, 'D')
    .toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60) || 'trang'
}

const NEW_BODY: Record<string, (t: string) => string> = {
  md: t => `# ${t}\n\n`,
  html: t => `<!doctype html>\n<html lang="vi">\n<head>\n<meta charset="utf-8">\n<title>${t}</title>\n</head>\n<body>\n<h1>${t}</h1>\n\n</body>\n</html>\n`,
  txt: () => '',
}

export default function WikiPanel({ call, channelID, channelName, onClose }: {
  call: Call; channelID: string; channelName: string; onClose: () => void
}) {
  const [pages, setPages] = useState<Page[]>([])
  const [baseURL, setBaseURL] = useState('')
  const [dir, setDir] = useState('')
  const [sel, setSel] = useState<string | null>(null)
  const [body, setBody] = useState('')
  const [saved, setSaved] = useState('') // body as last read/saved
  const [mtime, setMtime] = useState('')
  const [mode, setMode] = useState<'view' | 'edit' | 'split'>('view')
  const [filter, setFilter] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [newFmt, setNewFmt] = useState<'md' | 'html' | 'txt'>('md')
  const [dragging, setDragging] = useState(false)
  const dirty = sel !== null && body !== saved

  const load = useCallback(async () => {
    const r = await call('channels.wiki', { channel_id: channelID, op: 'list' })
    setPages(r?.pages ?? [])
    setBaseURL(r?.base_url ?? '')
    setDir(r?.dir ?? '')
    return (r?.pages ?? []) as Page[]
  }, [call, channelID])

  const open = useCallback(async (path: string, keepMode = false) => {
    setErr(null)
    try {
      const r = await call('channels.wiki', { channel_id: channelID, op: 'read', path })
      setSel(path); setBody(r.body); setSaved(r.body); setMtime(r.mtime)
      if (!keepMode) setMode('view')
    } catch (e: any) { setErr(String(e?.message ?? e)) }
  }, [call, channelID])

  useEffect(() => {
    load().then(ps => { if (ps.length) open(ps[0].path) }).catch(e => setErr(String(e?.message ?? e)))
  }, [load, open])

  const confirmLeave = () => !dirty || confirm('Trang đang sửa chưa lưu. Bỏ thay đổi?')

  const save = useCallback(async (overwrite = false) => {
    if (!sel || busy) return
    setBusy(true); setErr(null)
    try {
      const r = await call('channels.wiki', {
        channel_id: channelID, op: 'save', path: sel, body, base_mtime: overwrite ? '' : mtime,
      })
      setSaved(body); setMtime(r.mtime)
      await load()
    } catch (e: any) {
      const msg = String(e?.message ?? e)
      if (msg.includes('conflict') && confirm('Trang vừa được người khác (hoặc agent) sửa. Ghi đè bằng bản của bạn?')) {
        setBusy(false)
        return save(true)
      }
      setErr(msg)
    } finally { setBusy(false) }
  }, [sel, busy, call, channelID, body, mtime, load])

  // ⌘S / Ctrl+S saves; Escape closes when nothing is unsaved.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') { e.preventDefault(); save() }
      if (e.key === 'Escape' && !creating && confirmLeave()) onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const create = async () => {
    const title = newName.trim()
    if (!title) return
    let path = PAGE_EXT.test(title) ? title : `${slugify(title)}.${newFmt}`
    if (pages.some(p => p.path === path)) path = path.replace(/(\.[^.]+)$/, `-${Date.now() % 10000}$1`)
    const fmt = formatOf(path)
    const starter = (NEW_BODY[fmt] ?? NEW_BODY.txt)(PAGE_EXT.test(title) ? title.replace(PAGE_EXT, '') : title)
    try {
      await call('channels.wiki', { channel_id: channelID, op: 'save', path, body: starter })
      setCreating(false); setNewName('')
      await load()
      await open(path)
      setMode('split')
    } catch (e: any) { setErr(String(e?.message ?? e)) }
  }

  const remove = async () => {
    if (!sel || !confirm(`Xoá trang ${sel}?`)) return
    try {
      await call('channels.wiki', { channel_id: channelID, op: 'delete', path: sel })
      setSel(null); setBody(''); setSaved('')
      const ps = await load()
      if (ps.length) open(ps[0].path)
    } catch (e: any) { setErr(String(e?.message ?? e)) }
  }

  const rename = async () => {
    if (!sel) return
    const to = prompt('Tên mới (giữ đuôi .md / .html …)', sel)?.trim()
    if (!to || to === sel) return
    try {
      await call('channels.wiki', { channel_id: channelID, op: 'rename', path: sel, to })
      await load(); await open(to, true)
    } catch (e: any) { setErr(String(e?.message ?? e)) }
  }

  // Files dropped on the panel become pages: a report saved on the desktop,
  // an export from another tool. Text formats only; the server refuses others.
  const importFiles = async (files: File[]) => {
    let last = ''
    for (const f of files) {
      if (!PAGE_EXT.test(f.name)) { setErr(`${f.name}: chỉ nhận .md .html .txt .json .csv .svg`); continue }
      const path = f.name.replace(/[^\w.\-]+/g, '-')
      try {
        await call('channels.wiki', { channel_id: channelID, op: 'save', path, body: await f.text() })
        last = path
      } catch (e: any) { setErr(`${f.name}: ${String(e?.message ?? e)}`) }
    }
    await load()
    if (last) open(last)
  }

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return pages
      .filter(p => p.format !== 'other' || p.path.endsWith('.svg'))
      .filter(p => !q || p.title.toLowerCase().includes(q) || p.path.toLowerCase().includes(q))
  }, [pages, filter])

  const fmt = sel ? formatOf(sel) : ''
  const hostURL = sel && baseURL ? baseURL + sel.split('/').map(encodeURIComponent).join('/') : ''

  const preview = (
    fmt === 'html' || (fmt !== 'md' && looksLikeHTML(body))
      ? <HtmlFrame html={body} title={sel ?? ''} />
      : fmt === 'md'
        ? <div className="h-full overflow-y-auto px-8 py-6"><MessageMarkdown wide>{body}</MessageMarkdown></div>
        : fmt === 'svg'
          ? <div className="h-full overflow-auto p-6 bg-white"><img alt={sel ?? ''} src={`data:image/svg+xml;charset=utf-8,${encodeURIComponent(body)}`} /></div>
          : <pre className="h-full overflow-auto px-6 py-4 text-xs text-gray-300 font-mono whitespace-pre-wrap">{body}</pre>
  )
  const editor = (
    <textarea
      value={body}
      onChange={e => setBody(e.target.value)}
      spellCheck={false}
      className="h-full w-full resize-none bg-gray-950 px-4 py-3 font-mono text-[13px] leading-5 text-gray-200 outline-none"
    />
  )

  return (
    <div className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-3 sm:p-6"
      onClick={() => { if (confirmLeave()) onClose() }}>
      <div
        onClick={e => e.stopPropagation()}
        onDragOver={e => { if (Array.from(e.dataTransfer.types).includes('Files')) { e.preventDefault(); setDragging(true) } }}
        onDragLeave={e => { if (e.currentTarget === e.target) setDragging(false) }}
        onDrop={e => { e.preventDefault(); setDragging(false); importFiles(Array.from(e.dataTransfer.files)) }}
        className={`relative flex h-[92vh] w-full max-w-7xl flex-col overflow-hidden rounded-xl bg-gray-950 ring-1 ${dragging ? 'ring-2 ring-sky-400' : 'ring-gray-800'} shadow-2xl`}
      >
        {dragging && (
          <div className="pointer-events-none absolute inset-0 z-10 grid place-items-center bg-sky-500/10 text-sky-200">
            Thả file .md / .html vào để thêm vào Wiki
          </div>
        )}
        <header className="flex items-center gap-3 border-b border-gray-800 px-4 py-2.5">
          <span className="text-sm font-medium text-gray-100">📚 Wiki · {channelName}</span>
          <a href={baseURL} target="_blank" rel="noopener noreferrer" title={dir}
            className="text-[11px] text-gray-500 hover:text-sky-300 font-mono truncate">{baseURL}</a>
          <button onClick={() => { if (confirmLeave()) onClose() }} className="ml-auto text-xs text-gray-500 hover:text-gray-300">đóng</button>
        </header>

        <div className="flex min-h-0 flex-1">
          {/* Pages */}
          <aside className="flex w-64 shrink-0 flex-col border-r border-gray-800">
            <div className="space-y-2 p-2">
              <input value={filter} onChange={e => setFilter(e.target.value)} placeholder="Tìm trang…"
                className="w-full rounded bg-gray-900 px-2 py-1 text-xs text-gray-200 ring-1 ring-gray-800 outline-none focus:ring-gray-600" />
              {creating ? (
                <div className="space-y-1.5 rounded bg-gray-900 p-2 ring-1 ring-gray-800">
                  <input autoFocus value={newName} onChange={e => setNewName(e.target.value)}
                    onKeyDown={e => { if (e.key === 'Enter' && !e.nativeEvent.isComposing) create(); if (e.key === 'Escape') setCreating(false) }}
                    placeholder="Tên trang" className="w-full rounded bg-gray-950 px-2 py-1 text-xs text-gray-200 outline-none ring-1 ring-gray-800" />
                  <div className="flex items-center gap-1">
                    {(['md', 'html', 'txt'] as const).map(f => (
                      <button key={f} onClick={() => setNewFmt(f)}
                        className={`rounded px-1.5 py-0.5 text-[11px] font-mono ${newFmt === f ? 'bg-sky-500/20 text-sky-300' : 'text-gray-500 hover:text-gray-300'}`}>.{f}</button>
                    ))}
                    <button onClick={create} disabled={!newName.trim()} className="ml-auto rounded bg-gray-100 px-2 py-0.5 text-[11px] text-gray-900 disabled:opacity-40">tạo</button>
                  </div>
                </div>
              ) : (
                <button onClick={() => setCreating(true)} className="w-full rounded px-2 py-1 text-left text-xs text-gray-400 ring-1 ring-gray-800 hover:text-sky-300">
                  + Trang mới
                </button>
              )}
            </div>
            <div className="flex-1 overflow-y-auto px-1 pb-2">
              {shown.length === 0 && (
                <p className="px-2 py-3 text-[11px] leading-5 text-gray-600">
                  Chưa có trang nào. Tạo trang mới, kéo file .md/.html vào đây, hoặc nhờ agent:
                  <code className="mt-1 block break-all text-gray-500">bomclaw wiki put --channel {channelID} --path x.md --file …</code>
                </p>
              )}
              {shown.map(p => (
                <button key={p.path}
                  onClick={() => { if (p.path !== sel && confirmLeave()) open(p.path) }}
                  className={`block w-full rounded px-2 py-1.5 text-left ${p.path === sel ? 'bg-gray-800' : 'hover:bg-gray-900'}`}>
                  <div className="flex items-center gap-1.5">
                    <span className={`shrink-0 rounded px-1 text-[9px] font-mono uppercase ${p.format === 'html' ? 'bg-orange-500/15 text-orange-300' : p.format === 'md' ? 'bg-sky-500/15 text-sky-300' : 'bg-gray-700/50 text-gray-400'}`}>{p.format === 'other' ? 'svg' : p.format}</span>
                    <span className="truncate text-xs text-gray-200">{p.title}</span>
                  </div>
                  <div className="mt-0.5 truncate pl-0.5 text-[10px] text-gray-600 font-mono">{p.path} · {ago(p.mtime)}</div>
                </button>
              ))}
            </div>
          </aside>

          {/* Page */}
          <section className="flex min-w-0 flex-1 flex-col">
            {sel ? (
              <>
                <div className="flex items-center gap-2 border-b border-gray-800 px-3 py-1.5 text-xs">
                  <span className="min-w-0 truncate font-mono text-gray-400">{sel}{dirty && <span className="text-amber-300"> ●</span>}</span>
                  <div className="ml-auto flex shrink-0 overflow-hidden rounded ring-1 ring-gray-700">
                    {([['view', 'Xem'], ['split', 'Chia đôi'], ['edit', 'Sửa']] as const).map(([m, label]) => (
                      <button key={m} onClick={() => setMode(m)}
                        className={`px-2 py-0.5 ${mode === m ? 'bg-gray-700 text-gray-100' : 'text-gray-400 hover:text-gray-200'}`}>{label}</button>
                    ))}
                  </div>
                  <button onClick={() => save()} disabled={!dirty || busy} title="Lưu (⌘S)"
                    className="shrink-0 rounded bg-gray-100 px-2 py-0.5 text-gray-900 disabled:bg-gray-700 disabled:text-gray-400">
                    {busy ? 'đang lưu…' : 'Lưu'}
                  </button>
                  {hostURL && <a href={hostURL} target="_blank" rel="noopener noreferrer" title="Mở trang đã lưu (link chia sẻ được khi đã đăng nhập)"
                    className="shrink-0 text-gray-400 hover:text-sky-300">Mở ↗</a>}
                  <button onClick={rename} className="shrink-0 text-gray-500 hover:text-gray-300">Đổi tên</button>
                  <button onClick={remove} className="shrink-0 text-gray-500 hover:text-red-300">Xoá</button>
                </div>
                {err && <div className="border-b border-red-900/50 bg-red-950/40 px-3 py-1 text-[11px] text-red-300">{err}</div>}
                <div className="flex min-h-0 flex-1">
                  {mode !== 'view' && <div className={`min-w-0 ${mode === 'split' ? 'w-1/2 border-r border-gray-800' : 'flex-1'}`}>{editor}</div>}
                  {mode !== 'edit' && <div className={`min-w-0 ${mode === 'split' ? 'w-1/2' : 'flex-1'}`}>{preview}</div>}
                </div>
              </>
            ) : (
              <div className="grid flex-1 place-items-center text-sm text-gray-600">
                {err ? <span className="text-red-300">{err}</span> : 'Chọn một trang, hoặc tạo trang mới.'}
              </div>
            )}
          </section>
        </div>
      </div>
    </div>
  )
}

/** Saves an artifact's body as a wiki page and returns the page path. */
export async function saveArtifactToWiki(call: Call, channelID: string, title: string, body: string): Promise<string> {
  const ext = looksLikeHTML(body) || /\.html?$/i.test(title) ? 'html' : 'md'
  const base = PAGE_EXT.test(title) ? title.replace(/\.[^.]+$/, '') : title
  let path = `${slugify(base)}.${ext}`
  const list = await call('channels.wiki', { channel_id: channelID, op: 'list' })
  if ((list?.pages ?? []).some((p: Page) => p.path === path)) path = path.replace(`.${ext}`, `-${Date.now() % 100000}.${ext}`)
  await call('channels.wiki', { channel_id: channelID, op: 'save', path, body })
  return path
}
