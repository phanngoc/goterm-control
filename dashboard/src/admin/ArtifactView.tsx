import { useEffect, useState } from 'react'
import type { Artifact } from './types'
import MessageMarkdown from '../components/MessageMarkdown'

type Call = (method: string, params?: any) => Promise<any>

export function isPage(a: Artifact): boolean {
  return a.content_type?.includes('html') === true || a.title.toLowerCase().endsWith('.html')
}

// ArtifactModal is where a report is actually read: wide, scrollable, and
// rendered rather than shown as source. Escape and the backdrop both close it,
// because a modal you can only leave through one small button is a modal that
// feels like a trap.
//
// Shared by the room and the task board. It lived inside the room, which meant
// a task's own output — the thing the work produced — could only be opened by
// finding the conversation that started it.
export function ArtifactModal({ a, content, truncated, onClose }: {
  a: Artifact; content: string; truncated: boolean; onClose: () => void
}) {
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
        className="w-full max-w-4xl max-h-[88vh] flex flex-col rounded-xl bg-gray-950 ring-1 ring-gray-800 shadow-2xl"
      >
        <header className="flex items-baseline gap-3 px-5 py-3 border-b border-gray-800">
          <span className="text-sm text-gray-200 font-medium truncate">{a.title}</span>
          <span className="text-[11px] text-gray-600 font-mono">{a.kind} · {a.bytes} bytes</span>
          <span className="ml-auto text-[11px] text-gray-600 font-mono">{a.id}</span>
          <button onClick={onClose} className="text-xs text-gray-500 hover:text-gray-300">close</button>
        </header>
        <div className="flex-1 overflow-y-auto px-6 py-4">
          <MessageMarkdown wide>{content}</MessageMarkdown>
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
      if (isPage(a)) {
        const blob = new Blob([body], { type: 'text/html;charset=utf-8' })
        const url = URL.createObjectURL(blob)
        window.open(url, '_blank', 'noopener')
        setTimeout(() => URL.revokeObjectURL(url), 60_000)
        return
      }
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
