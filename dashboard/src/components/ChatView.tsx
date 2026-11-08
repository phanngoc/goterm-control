import { useState, useRef, useEffect } from 'react'
import { useStore, ChatMessage, TodoItem } from '../stores/store'
import { eventsToMessages } from '../lib/transcript'
import MessageMarkdown from './MessageMarkdown'
import ChatInput, { type ChatInputHandle } from './ChatInput'
import ChatHeader from './ChatHeader'

export default function ChatView({ call }: { call: (m: string, p?: any) => Promise<any> }) {
  const messages = useStore(s => s.messages)
  const addMessage = useStore(s => s.addMessage)
  const setMessages = useStore(s => s.setMessages)
  const streamingThinking = useStore(s => s.streamingThinking)
  const streamingTodos = useStore(s => s.streamingTodos)
  const sending = useStore(s => s.sending)
  const setSending = useStore(s => s.setSending)
  const activeSessionId = useStore(s => s.activeSessionId)
  const [input, setInput] = useState('')
  const bottomRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<ChatInputHandle>(null)

  // Auto-scroll to bottom
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  // Focus input
  useEffect(() => {
    inputRef.current?.focus()
  }, [activeSessionId])

  // While the last message is a partial snapshot (bot still replying after a
  // page reload), poll the transcript every 10s to show fresh progress.
  useEffect(() => {
    const last = messages[messages.length - 1]
    if (!last?.partial || !activeSessionId || sending) return
    const t = setInterval(async () => {
      try {
        const events = await call('transcript.get', { session_id: activeSessionId })
        if (Array.isArray(events)) setMessages(eventsToMessages(events))
      } catch {}
    }, 10_000)
    return () => clearInterval(t)
  }, [messages, activeSessionId, sending, call, setMessages])

  const resetStreaming = useStore(s => s.resetStreaming)
  const streamingText = useStore(s => s.streamingText)
  const streamingTools = useStore(s => s.streamingTools)

  const send = async () => {
    const text = input.trim()
    if (!text || sending) return
    setInput('')

    addMessage({ role: 'user', content: text, timestamp: new Date().toISOString() })
    setSending(true)
    resetStreaming()

    try {
      const result = await call('send', {
        message: text,
        session_id: activeSessionId === 'new' ? undefined : activeSessionId,
      })
      // Use streamed text if available (more complete), else use result
      const finalText = useStore.getState().streamingText || result.text || '(no response)'
      const tools = useStore.getState().streamingTools
      addMessage({
        role: 'assistant',
        content: finalText,
        timestamp: new Date().toISOString(),
        tools: tools.length > 0 ? tools : undefined,
      })
    } catch (e: any) {
      const streamedSoFar = useStore.getState().streamingText
      addMessage({
        role: 'assistant',
        content: streamedSoFar || `Error: ${e.message}`,
        timestamp: new Date().toISOString(),
      })
    } finally {
      setSending(false)
      resetStreaming()
    }
  }

  return (
    <div className="h-full flex flex-col">
      <ChatHeader call={call} />
      {/* Messages */}
      <div className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
        {messages.length === 0 && (
          <div className="flex items-center justify-center h-full text-gray-500">
            <p>Send a message to start</p>
          </div>
        )}
        {messages.map((msg, i) => (
          <MessageBubble key={i} message={msg} />
        ))}
        {sending && (
          <div className="flex gap-2 items-start">
            <div className="w-7 h-7 rounded-full bg-violet-600 flex items-center justify-center text-xs shrink-0">N</div>
            <div className="max-w-[80%] px-3 py-2 rounded-xl bg-gray-800 text-gray-200 text-sm">
              <TurnProgress thinking={streamingThinking} tools={streamingTools} todos={streamingTodos} />
              {streamingText ? (
                <MessageMarkdown>{streamingText}</MessageMarkdown>
              ) : !streamingThinking && streamingTools.length === 0 ? (
                <span className="animate-pulse text-gray-400">Thinking...</span>
              ) : null}
            </div>
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {/* Input */}
      <div className="border-t border-gray-800 px-3 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] bg-gray-900">
        <ChatInput
          ref={inputRef}
          className="max-w-3xl mx-auto"
          value={input}
          onChange={setInput}
          onSend={send}
          disabled={sending}
          busy={sending}
          onStop={() => call('cancel').catch(() => {})}
          placeholder="Nhắn cho agent…"
        />
      </div>
    </div>
  )
}

function MessageBubble({ message }: { message: ChatMessage }) {
  const isUser = message.role === 'user'

  return (
    <div className={`flex gap-2 items-start ${isUser ? 'flex-row-reverse' : ''}`}>
      <div className={`w-7 h-7 rounded-full flex items-center justify-center text-xs shrink-0 ${
        isUser ? 'bg-blue-600' : 'bg-violet-600'
      }`}>
        {isUser ? 'U' : 'N'}
      </div>
      <div className={`max-w-[80%] rounded-xl px-3 py-2 text-sm ${
        isUser
          ? 'bg-blue-600 text-white'
          : 'bg-gray-800 text-gray-200'
      }`}>
        {message.tools && message.tools.length > 0 && (
          <div className="text-xs text-gray-400 mb-1">
            🔧 {compactTools(message.tools)}
          </div>
        )}
        {/* What a person typed is shown as typed — line breaks included —
            the way ChatGPT shows it; markdown would fold their lines together. */}
        {isUser
          ? <div className="whitespace-pre-wrap break-words">{message.content}</div>
          : <MessageMarkdown>{message.content}</MessageMarkdown>}
        {message.partial && (
          <div className="text-xs text-yellow-500/80 mt-1 animate-pulse">
            ⏳ đang trả lời… (bản lưu tạm mỗi 10s)
          </div>
        )}
        <div className="text-[10px] text-gray-500 mt-1">
          {new Date(message.timestamp).toLocaleTimeString()}
        </div>
      </div>
    </div>
  )
}

// Show first 5 unique tools + count of extras. Groups consecutive duplicates.
// e.g. ["Read(a.go)", "Read(b.go)", "Read(c.go)", "Bash(ls)"] → "Read ×3 → Bash(ls)"
function compactTools(tools: string[]): string {
  if (tools.length === 0) return ''
  const groups: { name: string; label: string; count: number }[] = []
  for (const t of tools) {
    const name = t.split('(')[0]
    const last = groups[groups.length - 1]
    if (last && last.name === name) {
      last.count++
      last.label = t
    } else {
      groups.push({ name, label: t, count: 1 })
    }
  }
  const shown = groups.slice(0, 5).map(g =>
    g.count > 1 ? `${g.name} ×${g.count}` : g.label
  )
  const remaining = groups.slice(5).reduce((sum, g) => sum + g.count, 0)
  if (remaining > 0) shown.push(`+${remaining} more`)
  return shown.join(' → ')
}

// TurnProgress is the turn while it runs, laid out like Claude Code's: the
// newest thinking dimmed, the steps taken with the running one last, and the
// model's own plan with what is done and what is left. It is what keeps a long
// turn from looking like silence.
function TurnProgress({ thinking, tools, todos }: { thinking: string; tools: string[]; todos: TodoItem[] }) {
  const [open, setOpen] = useState(false)
  const paras = thinking.trim().split(/\n\n+/).filter(Boolean)
  const latest = paras[paras.length - 1] ?? ''
  const shown = tools.slice(-6)
  const hidden = tools.length - shown.length
  const done = todos.filter(t => t.status === 'completed').length
  if (!latest && tools.length === 0 && todos.length === 0) return null
  return (
    <div className="mb-2 space-y-2 text-xs">
      {latest && (
        <button type="button" onClick={() => setOpen(v => !v)}
          className="block w-full text-left border-l-2 border-gray-600 pl-2 italic text-gray-400 hover:text-gray-300">
          <span className="not-italic">💭 </span>
          {open ? <span className="whitespace-pre-wrap">{thinking.trim()}</span> : <span className="line-clamp-3">{latest}</span>}
          {paras.length > 1 && <span className="not-italic text-gray-500"> {open ? '· thu gọn' : `· ${paras.length} đoạn`}</span>}
        </button>
      )}
      {tools.length > 0 && (
        <ul className="space-y-0.5 font-mono text-[11px] text-gray-400">
          {hidden > 0 && <li className="text-gray-500">… {hidden} bước trước</li>}
          {shown.map((t, i) => {
            const running = i === shown.length - 1
            return (
              <li key={hidden + i} className={`truncate ${running ? 'text-sky-300' : ''}`}>
                {running ? <span className="inline-block animate-pulse">▸</span> : '✓'} {t}
              </li>
            )
          })}
        </ul>
      )}
      {todos.length > 0 && (
        <div className="rounded-md bg-gray-900/60 px-2 py-1.5">
          <div className="mb-1 text-[11px] text-gray-400">Kế hoạch · {done}/{todos.length} xong</div>
          <ul className="space-y-0.5">
            {todos.map((t, i) => (
              <li key={i} className={t.status === 'completed' ? 'text-gray-500 line-through' : t.status === 'in_progress' ? 'text-gray-100 font-medium' : 'text-gray-400'}>
                {t.status === 'completed' ? '☑' : t.status === 'in_progress' ? '◐' : '☐'} {t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
