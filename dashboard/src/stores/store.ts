import { create } from 'zustand'

export type Session = {
  id: string
  chat_id: number
  claude_session_id?: string
  message_count: number
  input_tokens: number
  output_tokens: number
  created_at: string
  updated_at: string
}

export type TranscriptEvent = {
  type: 'user_message' | 'assistant_text' | 'tool_call' | 'tool_result' | 'session_start' | 'session_reset'
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
}

type Tab = 'chat' | 'sessions' | 'status'

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

  // Status
  status: any
  setStatus: (s: any) => void
}

export const useStore = create<Store>((set) => ({
  connected: false,
  setConnected: (connected) => set({ connected }),

  tab: 'sessions',
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

  status: null,
  setStatus: (status) => set({ status }),
}))
