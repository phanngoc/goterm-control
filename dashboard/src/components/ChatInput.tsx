import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { filesFrom } from './attachments'

// ChatInput is the one message box every chat surface uses, shaped like
// ChatGPT's: the text gets the whole width of a rounded card, the card grows
// with what is typed up to a cap and then scrolls inside, and the controls sit
// on a row beneath the text instead of beside it, so a long line wraps rather
// than being cut off behind a button.
//
// Enter sends, Shift+Enter breaks the line. Enter while an IME is composing
// (Vietnamese Telex, Japanese, Korean) commits the candidate and is not a send:
// Chrome reports that press as keyCode 229, isComposing covers the rest.

export interface ChatInputHandle { focus: () => void }

interface Props {
  value: string
  onChange: (s: string) => void
  onSend: () => void
  placeholder?: string
  /** false greys the send button out; an empty draft already does. */
  canSend?: boolean
  disabled?: boolean
  /** While true, the send button becomes Stop (when onStop is given). */
  busy?: boolean
  onStop?: () => void
  /** Left of the toolbar row: who it is for, attachments… */
  tools?: ReactNode
  /** Taller cap for a full page, shorter for a side panel. Pixels. */
  maxHeight?: number
  minRows?: number
  autoFocus?: boolean
  onFocus?: () => void
  className?: string
  /** Files dropped on the box or pasted into it. Without it the box takes text only. */
  onFiles?: (files: File[]) => void
  /** What is waiting to go with the text (chips), shown above it. */
  attachments?: ReactNode
  /** The draft has files, so it may be sent with no text. */
  hasFiles?: boolean
}

const ChatInput = forwardRef<ChatInputHandle, Props>(function ChatInput({
  value, onChange, onSend, placeholder, canSend = true, disabled, busy, onStop,
  tools, maxHeight, minRows = 1, autoFocus, onFocus, className = '',
  onFiles, attachments, hasFiles,
}, ref) {
  const [dragging, setDragging] = useState(false)
  const depth = useRef(0) // dragenter/leave fire for every child crossed
  const ta = useRef<HTMLTextAreaElement>(null)
  useImperativeHandle(ref, () => ({ focus: () => ta.current?.focus() }), [])

  // Grow to fit, up to the cap (default: 40% of the window, at least 8 lines),
  // then scroll inside. Measured from scrollHeight after collapsing to auto,
  // so deleting lines shrinks the box again.
  const fit = useCallback(() => {
    const el = ta.current
    if (!el) return
    const cap = maxHeight ?? Math.max(200, Math.round(window.innerHeight * 0.4))
    el.style.height = 'auto'
    const h = Math.min(el.scrollHeight, cap)
    el.style.height = `${h}px`
    el.style.overflowY = el.scrollHeight > cap ? 'auto' : 'hidden'
  }, [maxHeight])
  useLayoutEffect(fit, [value, fit])
  // Refit when the box's width changes, not only the window's: dragging the
  // editor's panel splitter rewraps the text without any resize event.
  useEffect(() => {
    const el = ta.current
    if (!el) return
    let w = el.clientWidth
    const ro = new ResizeObserver(() => {
      if (el.clientWidth !== w) { w = el.clientWidth; fit() }
    })
    ro.observe(el)
    window.addEventListener('resize', fit) // the cap follows the window height
    return () => { ro.disconnect(); window.removeEventListener('resize', fit) }
  }, [fit])
  useEffect(() => { if (autoFocus) ta.current?.focus() }, [autoFocus])

  const ready = canSend && !disabled && (value.trim() !== '' || !!hasFiles)
  const send = () => {
    if (!ready) return
    onSend()
    // Keep typing where you were: the box keeps focus after a send.
    requestAnimationFrame(() => ta.current?.focus())
  }

  return (
    <div
      onClick={e => { if (e.target === e.currentTarget) ta.current?.focus() }}
      // A file dragged over the box is taken by the box. Without this the
      // browser opens the picture in place of the dashboard.
      onDragEnter={onFiles && (e => {
        if (!Array.from(e.dataTransfer.types).includes('Files')) return
        e.preventDefault(); depth.current++; setDragging(true)
      })}
      onDragOver={onFiles && (e => {
        if (!Array.from(e.dataTransfer.types).includes('Files')) return
        e.preventDefault(); e.dataTransfer.dropEffect = 'copy'
      })}
      onDragLeave={onFiles && (() => {
        depth.current = Math.max(0, depth.current - 1)
        if (depth.current === 0) setDragging(false)
      })}
      onDrop={onFiles && (e => {
        depth.current = 0; setDragging(false)
        const files = filesFrom(e.dataTransfer)
        if (!files.length) return
        e.preventDefault()
        onFiles(files)
        ta.current?.focus()
      })}
      className={`relative rounded-2xl bg-gray-800/80 ring-1 ${
        dragging ? 'ring-2 ring-sky-400 bg-sky-500/10' : 'ring-gray-700 focus-within:ring-gray-500'
      } transition-shadow ${className}`}
    >
      {dragging && (
        <div className="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-2xl text-sm text-sky-200">
          Thả ảnh / tệp vào đây
        </div>
      )}
      {attachments}
      <textarea
        ref={ta}
        value={value}
        rows={minRows}
        disabled={disabled}
        onFocus={onFocus}
        onChange={e => onChange(e.target.value)}
        // A screenshot copied to the clipboard pastes as a file, not text.
        onPaste={onFiles && (e => {
          // Text copied from Excel or Word also carries a picture of itself;
          // the text is what was meant.
          if (e.clipboardData.getData('text/plain').trim()) return
          const files = filesFrom(e.clipboardData)
          if (!files.length) return
          e.preventDefault()
          onFiles(files)
        })}
        onKeyDown={e => {
          if (e.nativeEvent.isComposing || e.keyCode === 229) return
          if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
        }}
        placeholder={placeholder}
        // 16px on phones: iOS zooms the page into any smaller input on focus.
        className="block w-full resize-none bg-transparent px-4 pt-3 pb-1 text-base sm:text-sm leading-6 text-gray-100 placeholder:text-gray-500 outline-none whitespace-pre-wrap break-words disabled:opacity-60"
        style={{ overflowY: 'hidden' }}
      />
      <div className="flex items-center gap-2 px-2 pb-2 pt-1">
        {/* The controls keep their width; the hint gives way first, then
            disappears in a narrow side panel rather than sitting on them. */}
        <div className="flex min-w-0 shrink items-center gap-2 text-xs">{tools}</div>
        <span className="min-w-0 flex-1 truncate text-right text-[11px] text-gray-500 select-none">
          Shift+Enter xuống dòng{onFiles ? ' · kéo/dán ảnh' : ''}
        </span>
        {busy && onStop ? (
          <button
            type="button" onClick={onStop} title="Dừng"
            className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-gray-100 text-gray-900 hover:bg-white"
          >
            <span className="block h-3 w-3 rounded-[2px] bg-gray-900" />
          </button>
        ) : (
          <button
            type="button" onClick={send} disabled={!ready} title="Gửi (Enter)"
            className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-gray-100 text-gray-900 hover:bg-white disabled:bg-gray-600 disabled:text-gray-400 disabled:cursor-not-allowed"
          >
            {busy ? (
              <span className="block h-3.5 w-3.5 animate-spin rounded-full border-2 border-gray-900 border-t-transparent" />
            ) : (
              <svg viewBox="0 0 20 20" className="h-4 w-4" fill="currentColor" aria-hidden>
                <path d="M10 3.5a.75.75 0 0 1 .53.22l5 5a.75.75 0 1 1-1.06 1.06l-3.72-3.72V16a.75.75 0 0 1-1.5 0V6.06L5.53 9.78a.75.75 0 0 1-1.06-1.06l5-5A.75.75 0 0 1 10 3.5Z" />
              </svg>
            )}
          </button>
        )}
      </div>
    </div>
  )
})

export default ChatInput
