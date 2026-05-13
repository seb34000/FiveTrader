import { useState, useEffect, useRef, useCallback } from 'react'

const WS_URL = (() => {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws`
})()

const RECONNECT_DELAY_MS = 2000
const MAX_RECONNECT_DELAY_MS = 30000

export function useWebSocket() {
  const [data, setData] = useState(null)
  const [connected, setConnected] = useState(false)
  const wsRef = useRef(null)
  const reconnectDelay = useRef(RECONNECT_DELAY_MS)
  const unmounted = useRef(false)
  const reconnectTimer = useRef(null)

  const connect = useCallback(() => {
    if (unmounted.current) return

    const ws = new WebSocket(WS_URL)
    wsRef.current = ws

    ws.onopen = () => {
      if (unmounted.current) return
      setConnected(true)
      reconnectDelay.current = RECONNECT_DELAY_MS
    }

    ws.onmessage = (evt) => {
      if (unmounted.current) return
      try {
        setData(JSON.parse(evt.data))
      } catch { /* ignore parse errors */ }
    }

    ws.onclose = () => {
      if (unmounted.current) return
      setConnected(false)
      // Exponential backoff
      reconnectTimer.current = setTimeout(() => {
        reconnectDelay.current = Math.min(reconnectDelay.current * 1.5, MAX_RECONNECT_DELAY_MS)
        connect()
      }, reconnectDelay.current)
    }

    ws.onerror = () => {
      ws.close()
    }
  }, [])

  useEffect(() => {
    unmounted.current = false
    connect()
    return () => {
      unmounted.current = true
      clearTimeout(reconnectTimer.current)
      if (wsRef.current) wsRef.current.close()
    }
  }, [connect])

  return { data, connected }
}
