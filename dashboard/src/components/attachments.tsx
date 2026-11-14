import { useCallback, useRef, useState } from 'react'

// Files dropped or pasted into a message box.
//
// Each file is uploaded the moment it lands (POST /api/uploads/), so by the
// time Enter is pressed the agent's copy is already on disk. The message then
// carries the absolute paths in the same shape a Telegram photo arrives in —
// "📎 … dùng công cụ Read để mở" and one path per line — so every agent reads
// a dropped picture the way it already reads a sent one.

export interface Attachment {
  key: string
  name: string
  /** Local preview while it uploads; the served URL afterwards. */
  preview?: string
  isImage: boolean
  state: 'uploading' | 'ready' | 'error'
  path?: string
  url?: string
  error?: string
}

const ATTACH_HEADER = '📎 Đính kèm'

let seq = 0

async function upload(file: File): Promise<{ path: string; url: string }> {
  const res = await fetch('/api/uploads/', {
    method: 'POST',
    headers: {
      'Content-Type': file.type || 'application/octet-stream',
      'X-Filename': encodeURIComponent(file.name || 'image.png'),
    },
    body: file,
  })
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`)
  // A server without this route answers with the app's index.html and 200.
  if (!(res.headers.get('Content-Type') ?? '').includes('application/json')) {
    throw new Error('máy chủ này chưa hỗ trợ tải tệp lên')
  }
  return res.json()
}

export function useAttachments() {
  const [items, setItems] = useState<Attachment[]>([])
  const alive = useRef(new Set<string>())

  const add = useCallback((files: File[]) => {
    for (const file of files) {
      const key = `a${++seq}`
      const isImage = file.type.startsWith('image/')
      alive.current.add(key)
      setItems(xs => [...xs, {
        key, name: file.name || 'image.png', isImage, state: 'uploading',
        preview: isImage ? URL.createObjectURL(file) : undefined,
      }])
      upload(file).then(
        r => {
          if (!alive.current.has(key)) return
          setItems(xs => xs.map(x => x.key === key ? { ...x, state: 'ready', path: r.path, url: r.url } : x))
        },
        e => {
          if (!alive.current.has(key)) return
          setItems(xs => xs.map(x => x.key === key ? { ...x, state: 'error', error: String(e?.message ?? e) } : x))
        },
      )
    }
  }, [])

  const remove = useCallback((key: string) => {
    alive.current.delete(key)
    setItems(xs => {
      const gone = xs.find(x => x.key === key)
      if (gone?.preview?.startsWith('blob:')) URL.revokeObjectURL(gone.preview)
      return xs.filter(x => x.key !== key)
    })
  }, [])

  const clear = useCallback(() => {
    alive.current.clear()
    setItems(xs => {
      xs.forEach(x => { if (x.preview?.startsWith('blob:')) URL.revokeObjectURL(x.preview) })
      return []
    })
  }, [])

  const uploading = items.some(x => x.state === 'uploading')
  const paths = items.filter(x => x.state === 'ready' && x.path).map(x => x.path!)
  return { items, add, remove, clear, uploading, paths }
}

/** The text a message ends with when it carries files. */
export function attachmentBlock(paths: string[]): string {
  if (!paths.length) return ''
  const what = paths.length === 1 ? '1 tệp' : `${paths.length} tệp`
  return `${ATTACH_HEADER} ${what} — hãy dùng công cụ Read để mở:\n` + paths.map(p => `- ${p}`).join('\n')
}

export interface PostedFile { path: string; url: string; name: string; isImage: boolean }

const UPLOAD_LINE = /^- (\/\S*\/goterm-shared\/uploads\/(\d{4}-\d{2}-\d{2})\/([^/\s]+))\s*$/
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp|heic|avif)$/i

/** Splits a posted message into its text and the files it carries. */
export function splitAttachments(body: string): { text: string; files: PostedFile[] } {
  const at = body.lastIndexOf(ATTACH_HEADER)
  if (at < 0) return { text: body, files: [] }
  const lines = body.slice(at).split('\n').slice(1)
  const files: PostedFile[] = []
  for (const l of lines) {
    const m = UPLOAD_LINE.exec(l)
    if (!m) return { text: body, files: [] } // not ours: show it as written
    const name = m[3].replace(/^[0-9a-f]{8}_/, '')
    files.push({
      path: m[1], name, isImage: IMAGE_EXT.test(m[3]),
      url: `/api/uploads/${m[2]}/${encodeURIComponent(m[3])}`,
    })
  }
  if (!files.length) return { text: body, files: [] }
  return { text: body.slice(0, at).trimEnd(), files }
}

/** Files a posted message carries, under its text. */
export function PostedFiles({ files }: { files: PostedFile[] }) {
  if (!files.length) return null
  return (
    <div className="mt-2 flex flex-wrap gap-2">
      {files.map(f => f.isImage ? (
        <a key={f.path} href={f.url} target="_blank" rel="noopener noreferrer" title={f.path}>
          <img
            src={f.url} alt={f.name} loading="lazy"
            className="max-h-48 max-w-[16rem] rounded-md object-contain ring-1 ring-gray-700 bg-gray-950"
          />
        </a>
      ) : (
        <a
          key={f.path} href={f.url} target="_blank" rel="noopener noreferrer" title={f.path}
          className="rounded-md bg-gray-800 px-2 py-1 text-xs text-sky-300 ring-1 ring-gray-700 hover:bg-gray-700"
        >
          📎 {f.name}
        </a>
      ))}
    </div>
  )
}

/** The files waiting in the box, with a way to take one back out. */
export function AttachmentChips({ items, onRemove }: { items: Attachment[]; onRemove: (key: string) => void }) {
  if (!items.length) return null
  return (
    <div className="flex flex-wrap gap-2 px-3 pt-3">
      {items.map(a => (
        <div
          key={a.key}
          title={a.error ? `Lỗi tải lên: ${a.error}` : a.path ?? a.name}
          className={`group relative h-16 rounded-lg ring-1 ${
            a.state === 'error' ? 'ring-red-500/60' : 'ring-gray-600'
          } bg-gray-900 overflow-hidden`}
        >
          {a.isImage && a.preview ? (
            <img src={a.preview} alt={a.name} className={`h-16 w-16 object-cover ${a.state === 'uploading' ? 'opacity-50' : ''}`} />
          ) : (
            <div className="flex h-16 w-28 items-center px-2 text-[11px] text-gray-300 break-all">📎 {a.name}</div>
          )}
          {a.state === 'uploading' && (
            <span className="absolute inset-0 m-auto block h-4 w-4 animate-spin rounded-full border-2 border-gray-200 border-t-transparent" />
          )}
          {a.state === 'error' && (
            <span className="absolute bottom-0 inset-x-0 bg-red-600/80 text-center text-[10px] text-white">lỗi</span>
          )}
          <button
            type="button" onClick={() => onRemove(a.key)} title="Bỏ tệp này"
            className="absolute right-0.5 top-0.5 grid h-5 w-5 place-items-center rounded-full bg-gray-950/80 text-xs text-gray-200 hover:bg-gray-700"
          >
            ×
          </button>
        </div>
      ))}
    </div>
  )
}

/** Files carried by a drag or a paste, if any. */
export function filesFrom(dt: DataTransfer | null): File[] {
  if (!dt) return []
  const out: File[] = []
  if (dt.items?.length) {
    for (const it of Array.from(dt.items)) {
      if (it.kind === 'file') { const f = it.getAsFile(); if (f) out.push(f) }
    }
  } else if (dt.files?.length) {
    out.push(...Array.from(dt.files))
  }
  return out
}
