import { ChatMessage, TranscriptEvent } from '../stores/store'

// Converts transcript events to chat messages. assistant_partial events are
// periodic snapshots of an in-progress reply: the final assistant_text of the
// turn supersedes them, so only a trailing partial (reply still streaming, or
// run interrupted) is shown — flagged with `partial: true`.
export function eventsToMessages(events: TranscriptEvent[]): ChatMessage[] {
  const msgs: ChatMessage[] = []
  let currentTools: string[] = []
  let partial: ChatMessage | null = null

  const flushPartial = () => {
    if (partial) {
      msgs.push(partial)
      partial = null
    }
  }

  for (const ev of events) {
    switch (ev.type) {
      case 'user_message':
        flushPartial()
        msgs.push({ role: 'user', content: ev.content || '', timestamp: ev.ts })
        break
      case 'tool_call':
        currentTools.push(ev.tool_name || '')
        break
      case 'assistant_partial':
        partial = {
          role: 'assistant',
          content: ev.content || '',
          timestamp: ev.ts,
          tools: currentTools.length > 0 ? [...currentTools] : undefined,
          partial: true,
        }
        break
      case 'assistant_text':
        partial = null // final supersedes this turn's partials
        msgs.push({
          role: 'assistant',
          content: ev.content || '',
          timestamp: ev.ts,
          tools: currentTools.length > 0 ? [...currentTools] : undefined,
        })
        currentTools = []
        break
    }
  }
  flushPartial()
  return msgs
}
