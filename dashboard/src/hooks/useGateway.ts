import { useEffect, useRef, useCallback } from 'react'
import { useStore } from '../stores/store'

// JSON-RPC over WebSocket — connects to the NanoClaw gateway
export function useGateway() {
  const ws = useRef<WebSocket | null>(null)
  const pending = useRef<Map<string, { resolve: (v: any) => void; reject: (e: Error) => void }>>(new Map())
  const idCounter = useRef(0)
  const setConnected = useStore(s => s.setConnected)

  const connect = useCallback(() => {
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${location.host}/ws`
    const socket = new WebSocket(url)
    ws.current = socket

    socket.onopen = () => setConnected(true)
    socket.onclose = () => {
      setConnected(false)
      setTimeout(connect, 3000) // auto-reconnect
    }
    socket.onerror = () => socket.close()

    socket.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data)
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
    return () => { ws.current?.close() }
  }, [connect])

  const call = useCallback(async <T = any>(method: string, params?: any): Promise<T> => {
    if (!ws.current || ws.current.readyState !== WebSocket.OPEN) {
      throw new Error('Not connected')
    }
    const id = String(++idCounter.current)
    return new Promise((resolve, reject) => {
      pending.current.set(id, { resolve, reject })
      ws.current!.send(JSON.stringify({ id, method, params }))
      setTimeout(() => {
        if (pending.current.has(id)) {
          pending.current.delete(id)
          reject(new Error('Timeout'))
        }
      }, 120_000)
    })
  }, [])

  return { call }
}
