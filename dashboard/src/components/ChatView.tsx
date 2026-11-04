import { useState, useRef, useEffect } from 'react'
import { useStore, ChatMessage } from '../stores/store'
import { eventsToMessages } from '../lib/transcript'
import MessageMarkdown from './MessageMarkdown'
import ChatInput, { type ChatInputHandle } from './ChatInput'

export default function ChatView({ call }: { call: (m: string, p?: any) => Promise<any> }) {
  const messages = useStore(s => s.messages)
  const addMessage = useStore(s => s.addMessage)
  const setMessages = useStore(s => s.setMessages)
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
              {streamingTools.length > 0 && (
                <div className="text-xs text-gray-400 mb-1">
                  🔧 {compactTools(streamingTools)}
                </div>
              )}
              {streamingText ? (
                <MessageMarkdown>{streamingText}</MessageMarkdown>
              ) : (
                <span className="animate-pulse text-gray-400">Thinking...</span>
              )}
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
