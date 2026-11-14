import { useEffect, useState } from 'react'
import { looksLikeHTML, HtmlFrame } from './WikiPanel'
import type { Artifact } from './types'
import MessageMarkdown from '../components/MessageMarkdown'

type Call = (method: string, params?: any) => Promise<any>

export function isPage(a: Artifact, body?: string): boolean {
  return a.content_type?.includes('html') === true || a.title.toLowerCase().endsWith('.html') ||
    (body !== undefined && looksLikeHTML(body))
}

// ArtifactModal is where a report is actually read: wide, scrollable, and
// rendered rather than shown as source. Escape and the backdrop both close it,
// because a modal you can only leave through one small button is a modal that
// feels like a trap.
//
// Shared by the room and the task board. It lived inside the room, which meant
// a task's own output — the thing the work produced — could only be opened by
// finding the conversation that started it.
export function ArtifactModal({ a, content, truncated, onClose, onSaveToWiki }: {
  a: Artifact; content: string; truncated: boolean; onClose: () => void
  /** Copies the artifact into the room's wiki; absent where there is no room. */
  onSaveToWiki?: () => Promise<string>
}) {
  // An HTML report is a page: shown as one, in a sandboxed frame, with the
  // source a click away. It used to be read as markdown — a wall of CSS.
  const html = isPage(a, content)
  const [source, setSource] = useState(false)
  const [saved, setSaved] = useState<string | null>(null)
  const openTab = () => {
    const url = URL.createObjectURL(new Blob([content], { type: 'text/html;charset=utf-8' }))
    window.open(url, '_blank', 'noopener')
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
  }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div
      onClick={onClose}
      className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-6"
    >
      <div
        onClick={e => e.stopPropagation()}
        className={`w-full ${html ? 'max-w-6xl h-[90vh]' : 'max-w-4xl max-h-[88vh]'} flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl`}
      >
        <header className="flex items-center gap-3 px-5 py-3 border-b border-gray-800">
          <span className="min-w-0 truncate text-sm text-gray-200 font-medium">{a.title}</span>
          <span className="shrink-0 text-[11px] text-gray-600 font-mono">{html ? 'html' : a.kind} · {a.bytes} bytes</span>
          <span className="ml-auto" />
          {html && (
            <>
              <button onClick={() => setSource(v => !v)} className="shrink-0 text-xs text-gray-400 hover:text-sky-300">
                {source ? 'Xem trang' : 'Mã nguồn'}
              </button>
              <button onClick={openTab} className="shrink-0 text-xs text-gray-400 hover:text-sky-300">Mở tab ↗</button>
            </>
          )}
          {onSaveToWiki && (
            <button
              disabled={saved !== null}
              onClick={async () => { try { setSaved(await onSaveToWiki()) } catch (e: any) { alert(String(e?.message ?? e)) } }}
              title="Lưu bản này vào Wiki của phòng"
              className="shrink-0 text-xs text-gray-400 hover:text-emerald-300 disabled:text-emerald-400"
            >
              {saved ? `✓ wiki/${saved}` : '→ Wiki'}
            </button>
          )}
          <button onClick={onClose} className="shrink-0 text-xs text-gray-500 hover:text-gray-300">đóng</button>
        </header>
        <div className={`flex-1 min-h-0 ${html && !source ? '' : 'overflow-y-auto px-6 py-4'}`}>
          {html && !source
            ? <HtmlFrame html={content} title={a.title} />
            : html
              ? <pre className="whitespace-pre-wrap break-words text-xs text-gray-300 font-mono">{content}</pre>
              : <MessageMarkdown wide>{content}</MessageMarkdown>}
          {truncated && (
            <p className="mt-4 text-xs text-amber-300">
              Bản xem trước bị cắt — đọc trọn vẹn bằng <code className="font-mono">bomclaw artifact get {a.id}</code>
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

/** useArtifactOpener gives a component one function that opens any artifact
 *  the right way, plus the modal to render.
 *
 *  Markdown and text open in the modal — these are reports, and reading one in
 *  a narrow panel is not reading it. HTML and links go to a tab: a page wants a
 *  browser, not a box inside one. */
export function useArtifactOpener(call: Call) {
  const [shown, setShown] = useState<Artifact | null>(null)
  const [content, setContent] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [busy, setBusy] = useState(false)

  const open = async (a: Artifact) => {
    setBusy(true)
    try {
      if (a.kind === 'link' && a.url) {
        window.open(a.url, '_blank', 'noopener')
        return
      }
      const r = await call('artifacts.get', { id: a.id })
      const body: string = r?.content ?? ''
      setShown(a)
      setContent(body)
      setTruncated(Boolean(r?.truncated))
    } catch (e) {
      alert(String(e))
    } finally {
      setBusy(false)
    }
  }

  const modal = shown && content !== null
    ? <ArtifactModal a={shown} content={content} truncated={truncated}
        onClose={() => { setShown(null); setContent(null) }} />
    : null

  return { open, modal, busy }
}
