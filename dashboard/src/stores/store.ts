import { create } from 'zustand'

export interface TodoItem {
  content: string
  status: 'pending' | 'in_progress' | 'completed' | string
  activeForm?: string
}


export type Session = {
  id: string
  chat_id: number
  claude_session_id?: string
  message_count: number
  input_tokens: number
  output_tokens: number
  created_at: string
  updated_at: string
  label?: string
  seq?: number
  account?: string
}

export type TranscriptEvent = {
  type: 'user_message' | 'assistant_text' | 'assistant_partial' | 'tool_call' | 'tool_result' | 'session_start' | 'session_reset'
  ts: string
  session_id?: string
  chat_id?: number
  content?: string
  tool_name?: string
  tool_input?: string
  is_error?: boolean
}

export type ChatMessage = {
  role: 'user' | 'assistant'
  content: string
  timestamp: string
  tools?: string[]
  partial?: boolean // reply still streaming (or interrupted) — snapshot only
}

type Tab = 'chat' | 'status' | 'admin' | 'files'

interface Store {
  // Connection
  connected: boolean
  setConnected: (v: boolean) => void

  // Navigation
  tab: Tab
  setTab: (t: Tab) => void

  // Sessions
  sessions: Session[]
  setSessions: (s: Session[]) => void
  activeSessionId: string | null
  setActiveSessionId: (id: string | null) => void

  // Chat
  messages: ChatMessage[]
  setMessages: (m: ChatMessage[]) => void
  addMessage: (m: ChatMessage) => void
  sending: boolean
  setSending: (v: boolean) => void

  // Streaming state
  streamingText: string
  streamingTools: string[]
  /** The model's thinking as it streams (Claude summaries, codex reasoning). */
  streamingThinking: string
  /** The model's own plan (TodoWrite), replaced whole on every revision. */
  streamingTodos: TodoItem[]
  streamingError: string | null
  appendStreamingText: (text: string) => void
  addStreamingTool: (name: string) => void
  appendStreamingThinking: (chunk: string) => void
  setStreamingTodos: (todos: TodoItem[]) => void
  setStreamingError: (err: string) => void
  resetStreaming: () => void

  // Status
  status: any
  setStatus: (s: any) => void

  // Activity on a session from another channel — a Telegram turn, a claimed
  // task. Set by the gateway hook when the server pushes session.turn;
  // consumed by App to refresh the open chat the moment it happens instead of
  // waiting for a reload.
  externalTurn: ExternalTurn | null
  setExternalTurn: (t: ExternalTurn | null) => void
}

export type ExternalTurn = {
  sessionId: string
  phase: 'started' | 'finished'
  kind?: 'chat' | 'task'   // absent = chat (older gateways)
  taskId?: string
  at: number
}

export const useStore = create<Store>((set) => ({
  connected: false,
  setConnected: (connected) => set({ connected }),

  tab: 'admin',
  setTab: (tab) => set({ tab }),

  sessions: [],
  setSessions: (sessions) => set({ sessions }),
  activeSessionId: null,
  setActiveSessionId: (activeSessionId) => set({ activeSessionId }),

  messages: [],
  setMessages: (messages) => set({ messages }),
  addMessage: (m) => set((s) => ({ messages: [...s.messages, m] })),
  sending: false,
  setSending: (sending) => set({ sending }),

  streamingText: '',
  streamingTools: [],
  streamingThinking: '',
  streamingTodos: [],
  streamingError: null,
  appendStreamingText: (text) => set((s) => ({ streamingText: s.streamingText + text })),
  addStreamingTool: (name) => set((s) => ({ streamingTools: [...s.streamingTools, name] })),
  // "" ends a thinking block: start the next one on a new paragraph.
  appendStreamingThinking: (chunk) => set((s) => ({
    streamingThinking: chunk === ''
      ? (s.streamingThinking && !s.streamingThinking.endsWith('\n\n') ? s.streamingThinking + '\n\n' : s.streamingThinking)
      : s.streamingThinking + chunk,
  })),
  setStreamingTodos: (streamingTodos) => set({ streamingTodos }),
  setStreamingError: (err) => set({ streamingError: err }),
  resetStreaming: () => set({ streamingText: '', streamingTools: [], streamingThinking: '', streamingTodos: [], streamingError: null }),

  status: null,
  setStatus: (status) => set({ status }),

  externalTurn: null,
  setExternalTurn: (externalTurn) => set({ externalTurn }),
}))
