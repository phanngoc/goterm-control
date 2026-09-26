import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import Editor, { type OnMount } from '@monaco-editor/react'
import { monaco } from './monacoSetup'
import MessageMarkdown from '../components/MessageMarkdown'
import { QuickOpen, SearchPanel, type Reveal } from './EditorFinders'
import { filesPath } from '../lib/route'

// xterm loads only when a terminal is first opened.
const TerminalPanel = lazy(() => import('./TerminalPanel'))

type Call = (method: string, params?: any) => Promise<any>

interface Entry {
  name: string
  path: string
  dir: boolean
  bytes: number
  mtime: string
}

interface Tab {
  path: string
  saved: string // what is on disk, as far as this editor knows
  mtime: string // when it was last read or written, for the conflict check
  truncated: boolean
  binary: boolean
  dirty: boolean
  conflict?: boolean
  preview?: boolean
}

const IMAGE = /\.(png|jpe?g|gif|webp|bmp|ico|avif)$/i
const isMarkdown = (p: string) => /\.(md|markdown)$/i.test(p)
const parentOf = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '')
const baseOf = (p: string) => p.slice(p.lastIndexOf('/') + 1)
const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name).replace(/\/+/g, '/').replace(/^\//, '')
const errText = (e: any) => String(e?.message ?? e)
const within = (p: string, prefix: string) => p === prefix || p.startsWith(prefix + '/')
// On a phone the tree and the editor cannot share the width.
const narrow = () => window.innerWidth < 768

// ProjectEditor is the project folder as an editor: tree, tabs, Monaco.
//
// Everything it does goes through channels.files, which the dashboard process
// answers itself from the project folder — so it keeps working while the agents
// restart, and it is confined to the folder by the same guard as before.
//
// Agents write in this folder while it is open. A save therefore carries the
// mtime the file was opened at and is refused if the file moved on since; the
// person then picks between the version on disk and their own.
export default function ProjectEditor({ call, channelID, root, onClose, standalone, initialFile, initialLine }: {
  call: Call; channelID: string; root: string; onClose: () => void
  /** standalone is the editor as its own page, /files/<channel>/<path>: it
   *  keeps the address bar pointing at the open file and line. */
  standalone?: boolean
  /** initialFile (and initialLine) open on arrival — a link to a spot. */
  initialFile?: string
  initialLine?: number
}) {
  const [dirs, setDirs] = useState<Record<string, Entry[]>>({})
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(['']))
  const [tabs, setTabs] = useState<Tab[]>([])
  const [active, setActive] = useState('')
  const [folder, setFolder] = useState('') // where "new file" and "new folder" land
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [cursor, setCursor] = useState({ line: 1, col: 1 })
  const [lang, setLang] = useState('')
  const [live, setLive] = useState('') // the active buffer, for the markdown preview
  const [sidebar, setSidebar] = useState(true)
  const [view, setView] = useState<'explorer' | 'search'>('explorer')
  const [quickOpen, setQuickOpen] = useState(false)
  const [searchFocus, setSearchFocus] = useState(0)
  // The terminal panel: mounted on first open and kept mounted after, so
  // hiding it does not end the shells in it.
  const [termOpen, setTermOpen] = useState(false)
  const [termMounted, setTermMounted] = useState(false)
  const [termMax, setTermMax] = useState(false)
  const [termHeight, setTermHeight] = useState(() => Math.round(window.innerHeight * 0.35))
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null)
  // A search hit waits here until its file's model is in the editor.
  const pendingReveal = useRef<{ path: string; at: Reveal } | null>(null)

  const tabsRef = useRef(tabs)
  tabsRef.current = tabs
  const activeRef = useRef(active)
  activeRef.current = active

  const project = root.split('/').pop() || 'project'
  const uriFor = useCallback((path: string) => monaco.Uri.parse(`file:///${channelID}/${path}`), [channelID])
  const modelFor = useCallback((path: string) => monaco.editor.getModel(uriFor(path)), [uriFor])
  const activeTab = tabs.find(t => t.path === active)

  const loadDir = useCallback(async (path: string) => {
    try {
      const r = await call('channels.files', { channel_id: channelID, path })
      setDirs(d => ({ ...d, [path]: r?.entries ?? [] }))
    } catch (e) {
      setErr(errText(e))
    }
  }, [call, channelID])

  useEffect(() => { loadDir('') }, [loadDir])

  // A link to a file opens it, with the tree unfolded down to it so you can see
  // where it sits. Once only: the link is where you arrived, not where you are.
  const arrived = useRef(false)
  useEffect(() => {
    if (arrived.current || !initialFile) return
    arrived.current = true
    const parents: string[] = []
    for (let d = parentOf(initialFile); d; d = parentOf(d)) parents.unshift(d)
    setExpanded(s => new Set([...s, ...parents]))
    parents.forEach(d => loadDir(d))
    openFile(initialFile, initialLine ? { line: initialLine, col: 1, len: 0 } : undefined)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialFile])

  // Models outlive the component unless disposed; a second open of the same
  // project would otherwise start from stale buffers.
  useEffect(() => () => {
    for (const m of monaco.editor.getModels()) {
      if (m.uri.path.startsWith(`/${channelID}/`)) m.dispose()
    }
  }, [channelID])

  const anyDirty = tabs.some(t => t.dirty)
  useEffect(() => {
    if (!anyDirty) return
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault() }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [anyDirty])

  const refresh = () => { for (const p of expanded) loadDir(p) }

  const toggleDir = (path: string) => {
    setFolder(path)
    const isOpen = expanded.has(path)
    setExpanded(s => {
      const n = new Set(s)
      if (isOpen) n.delete(path)
      else n.add(path)
      return n
    })
    if (!isOpen && !dirs[path]) loadDir(path)
  }

  const read = async (path: string) => {
    const r = await call('channels.files', { channel_id: channelID, path, read: true })
    return {
      path, saved: r?.body ?? '', mtime: r?.mtime ?? '',
      truncated: !!r?.truncated, binary: !!r?.binary, dirty: false,
    } as Tab
  }

  const revealIn = (ed: monaco.editor.IStandaloneCodeEditor, at: Reveal) => {
    ed.revealLineInCenter(at.line)
    ed.setSelection(new monaco.Range(at.line, at.col, at.line, at.col + at.len))
    ed.focus()
  }

  const openFile = async (path: string, at?: Reveal) => {
    setFolder(parentOf(path))
    if (narrow()) setSidebar(false)
    pendingReveal.current = at ? { path, at } : null
    if (tabsRef.current.some(t => t.path === path)) {
      if (path === activeRef.current && at && editorRef.current) {
        pendingReveal.current = null
        revealIn(editorRef.current, at)
      }
      setActive(path)
      return
    }
    try {
      const tab = await read(path)
      // A model left from a tab closed without disposing would show old text.
      modelFor(path)?.dispose()
      setTabs(ts => [...ts, tab])
      setActive(path)
      setErr(null)
    } catch (e) {
      setErr(errText(e))
    }
  }

  const closeTab = (path: string, ask = true) => {
    const tab = tabsRef.current.find(t => t.path === path)
    if (!tab) return
    if (ask && tab.dirty && !window.confirm(`${baseOf(path)} có thay đổi chưa lưu. Đóng và bỏ thay đổi?`)) return
    modelFor(path)?.dispose()
    const rest = tabsRef.current.filter(t => t.path !== path)
    setTabs(rest)
    if (activeRef.current === path) {
      const i = tabsRef.current.findIndex(t => t.path === path)
      setActive(rest[Math.min(i, rest.length - 1)]?.path ?? '')
    }
  }

  const save = async (force = false) => {
    const path = activeRef.current
    const tab = tabsRef.current.find(t => t.path === path)
    const model = modelFor(path)
    if (!tab || !model || tab.binary || tab.truncated) return
    const body = model.getValue()
    setSaving(true)
    try {
      const r = await call('channels.files', {
        channel_id: channelID, path, body,
        ...(force || !tab.mtime ? {} : { base_mtime: tab.mtime }),
      })
      setTabs(ts => ts.map(t => t.path === path
        ? { ...t, saved: body, mtime: r?.mtime ?? '', dirty: model.getValue() !== body, conflict: false }
        : t))
      setErr(null)
    } catch (e) {
      const msg = errText(e)
      if (msg.includes('conflict')) setTabs(ts => ts.map(t => t.path === path ? { ...t, conflict: true } : t))
      else setErr(msg)
    } finally {
      setSaving(false)
    }
  }
  const saveRef = useRef(save)
  saveRef.current = save

  // Take what is on disk, as an edit, so Cmd+Z still reaches the person's own
  // version if they change their mind.
  const reloadFromDisk = async (path: string) => {
    try {
      const fresh = await read(path)
      const model = modelFor(path)
      if (model) model.pushEditOperations([], [{ range: model.getFullModelRange(), text: fresh.saved }], () => null)
      setTabs(ts => ts.map(t => t.path === path ? { ...fresh, preview: t.preview } : t))
      if (path === activeRef.current) setLive(fresh.saved)
    } catch (e) {
      setErr(errText(e))
    }
  }

  // Cmd+P opens a file by name, Cmd+Shift+F searches every file.
  const findFile = () => setQuickOpen(true)
  const findInFiles = () => {
    setSidebar(true)
    setView('search')
    setSearchFocus(n => n + 1)
  }
  const findFileRef = useRef(findFile)
  findFileRef.current = findFile
  const findInFilesRef = useRef(findInFiles)
  findInFilesRef.current = findInFiles

  // The shortcuts anywhere in the editor window, not only with the cursor in
  // Monaco. Capture phase, so the browser's own Cmd+P (print) never opens.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Ctrl+` toggles the terminal, as in VS Code.
      if (e.ctrlKey && !e.metaKey && (e.code === 'Backquote' || e.key === '`')) {
        e.preventDefault(); e.stopPropagation(); toggleTermRef.current()
        return
      }
      if (!(e.metaKey || e.ctrlKey)) return
      // Inside a terminal, Ctrl+P, Ctrl+S and friends belong to the shell
      // (history, flow control…); only the Cmd variants are the editor's.
      if (!e.metaKey && (e.target as HTMLElement)?.closest?.('.xterm')) return
      const k = e.key.toLowerCase()
      if (k === 's' && !e.shiftKey) { e.preventDefault(); saveRef.current() }
      else if (k === 'p' && !e.shiftKey) { e.preventDefault(); e.stopPropagation(); findFileRef.current() }
      else if (k === 'f' && e.shiftKey) { e.preventDefault(); e.stopPropagation(); findInFilesRef.current() }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])

  const toggleTerm = () => {
    setTermMounted(true)
    setTermOpen(o => {
      if (o) editorRef.current?.focus()
      return !o
    })
  }
  const toggleTermRef = useRef(toggleTerm)
  toggleTermRef.current = toggleTerm

  // Dragging the bar above the terminal resizes it, within reason.
  const startDrag = (e: React.PointerEvent) => {
    e.preventDefault()
    const startY = e.clientY
    const startH = termHeight
    const move = (ev: PointerEvent) => {
      const h = startH + (startY - ev.clientY)
      setTermHeight(Math.max(120, Math.min(window.innerHeight - 160, h)))
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  const onMount: OnMount = (editor, m) => {
    editorRef.current = editor
    // The editor unmounts when no text file is showing; a stale handle would
    // be a disposed editor.
    editor.onDidDispose(() => { if (editorRef.current === editor) editorRef.current = null })
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.KeyS, () => saveRef.current())
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.KeyP, () => findFileRef.current())
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyMod.Shift | m.KeyCode.KeyF, () => findInFilesRef.current())
    editor.onDidChangeCursorPosition(e => setCursor({ line: e.position.lineNumber, col: e.position.column }))
    // Opening a file is a request to work in it: the cursor goes there, not
    // back to the tree row or button that was clicked.
    const syncModel = () => {
      const model = editor.getModel()
      setLang(model?.getLanguageId() ?? '')
      setLive(model?.getValue() ?? '')
      // Or the status bar and the address keep the last file's line.
      const pos = editor.getPosition()
      setCursor({ line: pos?.lineNumber ?? 1, col: pos?.column ?? 1 })
      editor.focus()
      const pending = pendingReveal.current
      if (pending && model && model.uri.toString() === uriFor(pending.path).toString()) {
        pendingReveal.current = null
        revealIn(editor, pending.at)
      }
    }
    editor.onDidChangeModel(syncModel)
    syncModel()
  }

  const onChange = (value: string | undefined) => {
    const path = activeRef.current
    const v = value ?? ''
    setLive(v)
    setTabs(ts => {
      const t = ts.find(x => x.path === path)
      if (!t || t.dirty === (v !== t.saved)) return ts
      return ts.map(x => x.path === path ? { ...x, dirty: v !== x.saved } : x)
    })
  }

  // --- tree operations ---

  const ensureOpen = (dir: string) => {
    setExpanded(s => {
      const n = new Set(s)
      let d = dir
      while (d) { n.add(d); d = parentOf(d) }
      return n
    })
  }

  const newFile = async () => {
    const name = window.prompt(`File mới trong ${folder ? folder + '/' : project + '/'}`)?.trim()
    if (!name) return
    const path = join(folder, name)
    try {
      // channels.files writes over an existing file, so ask first.
      const exists = await call('channels.files', { channel_id: channelID, path, read: true }).then(() => true, () => false)
      if (exists) { setErr(`${path} đã tồn tại`); return }
      await call('channels.files', { channel_id: channelID, path, body: '' })
      ensureOpen(parentOf(path))
      await loadDir(parentOf(path))
      await openFile(path)
    } catch (e) {
      setErr(errText(e))
    }
  }

  const newFolder = async () => {
    const name = window.prompt(`Thư mục mới trong ${folder ? folder + '/' : project + '/'}`)?.trim()
    if (!name) return
    const path = join(folder, name)
    try {
      await call('channels.files', { channel_id: channelID, path, op: 'mkdir' })
      ensureOpen(parentOf(path))
      await loadDir(parentOf(path))
      setFolder(path)
    } catch (e) {
      setErr(errText(e))
    }
  }

  const rename = async (e: Entry) => {
    const affected = tabsRef.current.filter(t => within(t.path, e.path))
    if (affected.some(t => t.dirty)) { setErr(`Lưu hoặc đóng các file đang sửa trong ${e.path} trước khi đổi tên`); return }
    const to = window.prompt('Đổi tên / chuyển tới', e.path)?.trim().replace(/^\/+/, '')
    if (!to || to === e.path) return
    try {
      await call('channels.files', { channel_id: channelID, path: e.path, to, op: 'rename' })
      const reopen = affected.map(t => to + t.path.slice(e.path.length))
      affected.forEach(t => closeTab(t.path, false))
      ensureOpen(parentOf(to))
      await Promise.all([loadDir(parentOf(e.path)), loadDir(parentOf(to))])
      for (const p of reopen) await openFile(p)
    } catch (x) {
      setErr(errText(x))
    }
  }

  const remove = async (e: Entry) => {
    const affected = tabsRef.current.filter(t => within(t.path, e.path))
    const unsaved = affected.some(t => t.dirty) ? ' (có thay đổi chưa lưu sẽ mất)' : ''
    if (!window.confirm(`Xoá ${e.dir ? 'thư mục' : 'file'} ${e.path}${e.dir ? ' và mọi thứ bên trong' : ''}${unsaved}?`)) return
    try {
      await call('channels.files', { channel_id: channelID, path: e.path, op: 'delete' })
      affected.forEach(t => closeTab(t.path, false))
      if (e.dir) setDirs(d => Object.fromEntries(Object.entries(d).filter(([k]) => !within(k, e.path))))
      if (within(folder, e.path)) setFolder(parentOf(e.path))
      await loadDir(parentOf(e.path))
    } catch (x) {
      setErr(errText(x))
    }
  }

  const close = () => {
    if (anyDirty && !window.confirm('Có file chưa lưu. Đóng editor và bỏ thay đổi?')) return
    onClose()
  }

  const renderDir = (path: string, depth: number): React.ReactNode => {
    const entries = dirs[path]
    if (!entries) return <div className="px-3 py-1 text-xs text-gray-600" style={{ paddingLeft: 12 + depth * 12 }}>…</div>
    if (entries.length === 0 && depth > 0) {
      return <div className="py-0.5 text-xs text-gray-600 italic" style={{ paddingLeft: 22 + depth * 12 }}>trống</div>
    }
    return entries.map(e => {
      const isActive = !e.dir && e.path === active
      const isFolder = e.dir && e.path === folder
      return (
        <div key={e.path}>
          <div
            onClick={() => (e.dir ? toggleDir(e.path) : openFile(e.path))}
            title={e.path}
            className={`group flex items-center gap-1 pr-1 py-[3px] text-[13px] cursor-pointer select-none ${
              isActive ? 'bg-sky-900/40 text-white' : isFolder ? 'bg-gray-800/60 text-gray-200' : 'text-gray-300 hover:bg-gray-800/50'}`}
            style={{ paddingLeft: 8 + depth * 12 }}
          >
            <span className="w-3 shrink-0 text-[10px] text-gray-500">{e.dir ? (expanded.has(e.path) ? '▾' : '▸') : ''}</span>
            <span className={`truncate ${e.dir ? 'text-sky-300' : ''}`}>{e.name}</span>
            {tabs.some(t => t.path === e.path && t.dirty) && <span className="text-amber-300 text-[10px]">●</span>}
            {/* On the selected row always, so a touch screen — no hover — can
                still reach them. */}
            <span className={`ml-auto gap-0.5 shrink-0 ${isActive || isFolder ? 'flex' : 'hidden group-hover:flex'}`}>
              <button
                title="Đổi tên / chuyển"
                onClick={ev => { ev.stopPropagation(); rename(e) }}
                className="px-1 text-[11px] text-gray-400 hover:text-sky-300"
              >đổi</button>
              <button
                title="Xoá"
                onClick={ev => { ev.stopPropagation(); remove(e) }}
                className="px-1 text-[11px] text-gray-400 hover:text-red-300"
              >xoá</button>
            </span>
          </div>
          {e.dir && expanded.has(e.path) && renderDir(e.path, depth + 1)}
        </div>
      )
    })
  }

  const projectURL = (path: string) =>
    `/project/${encodeURIComponent(channelID)}/${path.split('/').map(encodeURIComponent).join('/')}`

  const showPreview = !!activeTab?.preview && isMarkdown(active)

  // The address of exactly this spot: file and the line the cursor is on.
  const here = filesPath(channelID, active || undefined, active ? cursor.line : undefined)

  // As its own page, the address bar follows the editor, so copying it is
  // sharing where you are. replaceState: moving the cursor is not navigating,
  // and Back should leave the editor, not walk through every line visited.
  useEffect(() => {
    if (!standalone) return
    const t = setTimeout(() => {
      if (location.pathname + location.hash !== here) history.replaceState(null, '', here)
    }, 250)
    return () => clearTimeout(t)
  }, [standalone, here])

  useEffect(() => {
    if (!standalone) return
    const before = document.title
    document.title = `${active ? baseOf(active) + ' — ' : ''}${project}`
    return () => { document.title = before }
  }, [standalone, active, project])

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-[#1e1e1e] text-gray-200">
      {quickOpen && (
        <QuickOpen
          call={call} channelID={channelID} recent={tabs.map(t => t.path)}
          onOpen={p => { setQuickOpen(false); openFile(p) }}
          onClose={() => { setQuickOpen(false); editorRef.current?.focus() }}
        />
      )}
      {/* Title bar */}
      <header className="flex items-center gap-3 h-10 px-3 border-b border-black/40 bg-[#181818] text-sm shrink-0">
        <button onClick={() => setSidebar(s => !s)} title="Ẩn/hiện cây thư mục" className="text-gray-400 hover:text-white">☰</button>
        <span className="font-medium text-gray-100">{project}</span>
        <span className="hidden md:inline text-[11px] text-gray-500 font-mono truncate">{root}</span>
        <a href={projectURL('')} target="_blank" rel="noopener noreferrer" title="Mở trang của dự án (board, báo cáo…)" className="ml-auto text-xs text-gray-400 hover:text-sky-300">Mở ↗</a>
        {standalone ? (
          <button
            onClick={() => navigator.clipboard?.writeText(location.origin + here)}
            title="Chép link tới file và dòng đang mở"
            className="text-xs text-gray-400 hover:text-sky-300"
          >chép link</button>
        ) : (
          <a href={here} target="_blank" rel="noopener noreferrer" title="Mở editor ở tab riêng, tại file và dòng đang mở" className="text-xs text-gray-400 hover:text-sky-300">Tab riêng ↗</a>
        )}
        <button
          onClick={toggleTerm} title="Terminal trong thư mục dự án (Ctrl+`)"
          className={`text-xs ${termOpen ? 'text-sky-300' : 'text-gray-400 hover:text-white'}`}
        >Terminal</button>
        <button onClick={close} className="text-xs text-gray-400 hover:text-white">{standalone ? 'về phòng chat' : 'đóng'}</button>
      </header>

      {err && (
        <div className="flex items-center gap-3 px-3 py-1.5 text-xs bg-red-950/70 text-red-200 shrink-0">
          <span className="truncate">{err}</span>
          <button onClick={() => setErr(null)} className="ml-auto text-red-300 hover:text-white">×</button>
        </div>
      )}

      <div className="flex flex-1 min-h-0">
        {/* Explorer */}
        {sidebar && (
          <aside className="w-64 max-w-[45vw] shrink-0 flex flex-col border-r border-black/40 bg-[#181818]">
            <div className="flex items-center gap-2 px-3 h-8 text-[11px] uppercase tracking-wide shrink-0">
              <button
                onClick={() => setView('explorer')}
                className={view === 'explorer' ? 'text-gray-200' : 'text-gray-500 hover:text-gray-300'}
              >Explorer</button>
              <button
                onClick={findInFiles} title="Tìm trong mọi file (⌘⇧F)"
                className={view === 'search' ? 'text-gray-200' : 'text-gray-500 hover:text-gray-300'}
              >Search</button>
              {view === 'explorer' && (
                <span className="ml-auto flex gap-1 normal-case tracking-normal">
                  <button onClick={findFile} title="Mở file theo tên (⌘P)" className="px-1 text-gray-400 hover:text-white">⌘P</button>
                  <button onClick={newFile} title="File mới" className="px-1 text-gray-400 hover:text-white">+file</button>
                  <button onClick={newFolder} title="Thư mục mới" className="px-1 text-gray-400 hover:text-white">+dir</button>
                  <button onClick={refresh} title="Làm mới" className="px-1 text-gray-400 hover:text-white">⟳</button>
                </span>
              )}
            </div>
            {view === 'search' ? (
              <SearchPanel call={call} channelID={channelID} focusKey={searchFocus} onOpen={openFile} />
            ) : (<>
              <div
                onClick={() => setFolder('')}
                className={`px-3 py-1 text-[12px] font-medium cursor-pointer ${folder === '' ? 'text-gray-100' : 'text-gray-400'}`}
              >{project}</div>
              <div className="flex-1 overflow-y-auto pb-4">{renderDir('', 0)}</div>
            </>)}
          </aside>
        )}

        {/* Editor area */}
        <main className="flex-1 min-w-0 flex flex-col">
          {/* Tabs */}
          <div className="flex items-stretch h-9 bg-[#181818] overflow-x-auto shrink-0">
            {tabs.map(t => (
              <div
                key={t.path}
                onClick={() => setActive(t.path)}
                title={t.path}
                className={`group flex items-center gap-2 pl-3 pr-2 text-[13px] cursor-pointer border-r border-black/40 whitespace-nowrap ${
                  t.path === active ? 'bg-[#1e1e1e] text-white' : 'text-gray-400 hover:text-gray-200'}`}
              >
                <span>{baseOf(t.path)}</span>
                <button
                  onClick={e => { e.stopPropagation(); closeTab(t.path) }}
                  className="w-4 text-center text-gray-500 hover:text-white"
                  title="Đóng"
                >{t.dirty ? <span className="text-amber-300 group-hover:hidden">●</span> : null}<span className={t.dirty ? 'hidden group-hover:inline' : ''}>×</span></button>
              </div>
            ))}
            {activeTab && isMarkdown(active) && (
              <button
                onClick={() => setTabs(ts => ts.map(t => t.path === active ? { ...t, preview: !t.preview } : t))}
                className={`ml-auto px-3 text-xs ${activeTab.preview ? 'text-sky-300' : 'text-gray-400 hover:text-white'}`}
              >xem trước</button>
            )}
          </div>

          {activeTab?.conflict && (
            <div className="flex flex-wrap items-center gap-3 px-3 py-2 text-xs bg-amber-950/70 text-amber-100 shrink-0">
              <span>File đã bị sửa trên đĩa (có thể bởi một agent) sau khi bạn mở.</span>
              <button onClick={() => reloadFromDisk(active)} className="underline hover:text-white">Lấy bản trên đĩa</button>
              <button onClick={() => save(true)} className="underline hover:text-white">Ghi đè bằng bản của tôi</button>
              <button onClick={() => setTabs(ts => ts.map(t => t.path === active ? { ...t, conflict: false } : t))} className="ml-auto hover:text-white">để sau</button>
            </div>
          )}
          {activeTab?.truncated && (
            <div className="px-3 py-1.5 text-xs bg-gray-800 text-gray-300 shrink-0">
              File lớn hơn 512 KB — chỉ hiện phần đầu và không sửa được ở đây.
            </div>
          )}

          <div className="flex-1 min-h-0 flex" hidden={termOpen && termMax}>
            {!activeTab ? (
              <div className="flex-1 flex items-center justify-center text-sm text-gray-500">
                ⌘P mở file theo tên · ⌘⇧F tìm trong mọi file · ⌘S lưu
              </div>
            ) : activeTab.binary ? (
              <div className="flex-1 flex flex-col items-center justify-center gap-3 p-6 overflow-auto">
                {IMAGE.test(active)
                  ? <img src={projectURL(active)} alt={active} className="max-w-full max-h-[70vh] object-contain" />
                  : <p className="text-sm text-gray-500">File nhị phân — không mở được trong editor.</p>}
                <a href={projectURL(active)} target="_blank" rel="noopener noreferrer" className="text-xs text-sky-300 hover:underline">mở file ↗</a>
              </div>
            ) : (
              <>
                <div className={showPreview ? 'w-1/2 min-w-0' : 'flex-1 min-w-0'}>
                  <Editor
                    path={uriFor(active).toString()}
                    defaultValue={activeTab.saved}
                    theme="vs-dark"
                    onMount={onMount}
                    onChange={onChange}
                    options={{
                      readOnly: activeTab.truncated,
                      fontSize: 13,
                      minimap: { enabled: !narrow() },
                      automaticLayout: true,
                      scrollBeyondLastLine: false,
                      wordWrap: isMarkdown(active) ? 'on' : 'off',
                      tabSize: 2,
                      renderWhitespace: 'selection',
                    }}
                  />
                </div>
                {showPreview && (
                  <div className="w-1/2 min-w-0 overflow-y-auto border-l border-black/40 bg-gray-950 px-6 py-4">
                    <MessageMarkdown wide>{live}</MessageMarkdown>
                  </div>
                )}
              </>
            )}
          </div>

          {termMounted && (
            <div
              hidden={!termOpen}
              className={termMax ? 'flex-1 min-h-0 flex flex-col' : 'shrink-0 flex flex-col'}
              style={termMax ? undefined : { height: termHeight }}
            >
              {!termMax && (
                <div onPointerDown={startDrag} className="h-1 shrink-0 cursor-row-resize bg-black/40 hover:bg-sky-700" title="Kéo để đổi cỡ" />
              )}
              <div className="flex-1 min-h-0">
                <Suspense fallback={<div className="h-full flex items-center justify-center text-xs text-gray-500">Đang mở terminal…</div>}>
                  <TerminalPanel
                    channelID={channelID}
                    visible={termOpen}
                    onHide={() => { setTermOpen(false); editorRef.current?.focus() }}
                    maximized={termMax}
                    onToggleMax={() => setTermMax(m => !m)}
                  />
                </Suspense>
              </div>
            </div>
          )}

          {/* Status bar */}
          <footer className="flex items-center gap-4 h-6 px-3 text-[11px] bg-[#007acc] text-white shrink-0">
            <span className="truncate">{active || project}</span>
            {activeTab && !activeTab.binary && (
              <span className="ml-auto flex items-center gap-4 whitespace-nowrap">
                <span>Ln {cursor.line}, Col {cursor.col}</span>
                <span>{lang}</span>
                <span>{saving ? 'đang lưu…' : activeTab.truncated ? 'chỉ đọc' : activeTab.dirty ? 'chưa lưu' : 'đã lưu'}</span>
              </span>
            )}
          </footer>
        </main>
      </div>
    </div>
  )
}
