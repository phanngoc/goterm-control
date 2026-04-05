import { useEffect, useRef, useCallback } from 'react'
import { useStore } from '../stores/store'

export function useGateway() {
  const ws = useRef<WebSocket | null>(null)
  const pending = useRef<Map<string, { resolve: (v: any) => void; reject: (e: Error) => void }>>(new Map())
  const idCounter = useRef(0)
  const setConnected = useStore(s => s.setConnected)
  const ready = useRef(false)
  const waiters = useRef<Array<() => void>>([])

  const connect = useCallback(() => {
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${location.host}/ws`
    console.log('[gateway] connecting to', url)
    const socket = new WebSocket(url)
    ws.current = socket

    socket.onopen = () => {
      console.log('[gateway] connected')
      ready.current = true
      setConnected(true)
      waiters.current.forEach(fn => fn())
      waiters.current = []
    }

    socket.onclose = (e) => {
      console.log('[gateway] disconnected', e.code)
      ready.current = false
      setConnected(false)
      if (e.code !== 1000) setTimeout(connect, 3000)
    }

    socket.onerror = () => socket.close()

    socket.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data)

        // Handle streaming events (partial text/tool during "send")
        if (msg.type === 'stream') {
          const store = useStore.getState()
          if (msg.event === 'text') {
            store.appendStreamingText(msg.data)
          } else if (msg.event === 'tool') {
            store.addStreamingTool(msg.data)
          } else if (msg.event === 'error') {
            store.setStreamingError(msg.data)
          }
          return
        }

        // Handle normal RPC responses
        const p = pending.current.get(msg.id)
        if (p) {
          pending.current.delete(msg.id)
          if (msg.error) {
            p.reject(new Error(msg.error.message))
          } else {
            p.resolve(msg.result)
          }
        }
      } catch {}
    }
  }, [setConnected])

  useEffect(() => {
    connect()
    return () => { ready.current = false; ws.current?.close() }
  }, [connect])

  const call = useCallback(async <T = any>(method: string, params?: any): Promise<T> => {
    if (!ready.current) {
      await new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error('Connection timeout')), 10_000)
        waiters.current.push(() => { clearTimeout(timeout); resolve() })
      })
    }

    const id = String(++idCounter.current)
    return new Promise((resolve, reject) => {
      pending.current.set(id, { resolve, reject })
      ws.current!.send(JSON.stringify({ id, method, params }))
      // No timeout for send — streaming keeps it alive
      if (method !== 'send') {
        setTimeout(() => {
          if (pending.current.has(id)) {
            pending.current.delete(id)
            reject(new Error('Request timeout'))
          }
        }, 30_000)
      }
    })
  }, [])

  return { call }
}
