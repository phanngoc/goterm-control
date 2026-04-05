import { useState, useRef, useEffect } from 'react'
import { useStore, ChatMessage } from '../stores/store'
import Markdown from 'react-markdown'

export default function ChatView({ call }: { call: (m: string, p?: any) => Promise<any> }) {
  const messages = useStore(s => s.messages)
  const addMessage = useStore(s => s.addMessage)
  const sending = useStore(s => s.sending)
  const setSending = useStore(s => s.setSending)
  const activeSessionId = useStore(s => s.activeSessionId)
  const [input, setInput] = useState('')
  const bottomRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  // Auto-scroll to bottom
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  // Focus input
  useEffect(() => {
    inputRef.current?.focus()
  }, [activeSessionId])

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

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
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
                  🔧 {streamingTools.join(' → ')}
                </div>
              )}
              {streamingText ? (
                <div className="prose prose-sm prose-invert max-w-none [&_pre]:bg-gray-900 [&_pre]:rounded-lg [&_pre]:p-2 [&_code]:text-violet-300 [&_p]:my-1">
                  <Markdown>{streamingText}</Markdown>
                </div>
              ) : (
                <span className="animate-pulse text-gray-400">Thinking...</span>
              )}
            </div>
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {/* Input */}
      <div className="border-t border-gray-800 p-3 bg-gray-900">
        <div className="flex gap-2 max-w-3xl mx-auto">
          <textarea
            ref={inputRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Type a message... (Enter to send, Shift+Enter for newline)"
            rows={1}
            className="flex-1 bg-gray-800 text-gray-100 rounded-xl px-4 py-2.5 text-sm resize-none border border-gray-700 focus:border-blue-500 focus:outline-none placeholder-gray-500"
            disabled={sending}
          />
          <button
            onClick={send}
            disabled={sending || !input.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-xl text-sm font-medium hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            Send
          </button>
        </div>
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
            🔧 {message.tools.join(' → ')}
          </div>
        )}
        <div className="prose prose-sm prose-invert max-w-none [&_pre]:bg-gray-900 [&_pre]:rounded-lg [&_pre]:p-2 [&_code]:text-violet-300 [&_p]:my-1">
          <Markdown>{message.content}</Markdown>
        </div>
        <div className="text-[10px] text-gray-500 mt-1">
          {new Date(message.timestamp).toLocaleTimeString()}
        </div>
      </div>
    </div>
  )
}
