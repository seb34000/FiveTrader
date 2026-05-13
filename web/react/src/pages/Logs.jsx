import { useState, useEffect, useRef, useCallback } from 'react'
import './Logs.css'

// ── WebSocket connection ──────────────────────────────────────────────────────

const WS_URL = (() => {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws/logs`
})()

// ── Level config ──────────────────────────────────────────────────────────────

const LEVELS = {
  DEBUG: { color: 'var(--text3)',   bg: 'transparent',              short: 'DBG' },
  INFO:  { color: 'var(--blue)',    bg: 'rgba(88,166,255,.06)',      short: 'INF' },
  WARN:  { color: 'var(--yellow)',  bg: 'rgba(210,153,34,.08)',      short: 'WRN' },
  ERROR: { color: 'var(--red)',     bg: 'rgba(248,81,73,.1)',        short: 'ERR' },
  FATAL: { color: 'var(--red)',     bg: 'rgba(248,81,73,.15)',       short: 'FTL' },
}

// Tags that indicate trading-relevant entries (highlighted)
const TRADE_KEYWORDS = [
  'signal', 'executing', 'risk', 'approved', 'rejected', 'pnl', 'won',
  'oracle_lag', 'window_delta', 'dump_hedge', 'no signal', 'diag_',
  'circuit', 'settled', 'fill', 'order', 'execution',
]

function isTradeRelevant(entry) {
  const text = (entry.msg + JSON.stringify(entry.fields ?? {})).toLowerCase()
  return TRADE_KEYWORDS.some(kw => text.includes(kw))
}

// ── Field renderer ────────────────────────────────────────────────────────────

function Fields({ fields }) {
  if (!fields || Object.keys(fields).length === 0) return null
  return (
    <span className="log-fields">
      {Object.entries(fields).map(([k, v]) => (
        <span key={k} className="log-field">
          <span className="log-field-key">{k}</span>
          <span className="log-field-eq">=</span>
          <span className="log-field-val">{String(v)}</span>
        </span>
      ))}
    </span>
  )
}

// ── Main component ────────────────────────────────────────────────────────────

export default function Logs() {
  const [entries, setEntries] = useState([])      // all received entries
  const [paused, setPaused]   = useState(false)   // pause auto-scroll
  const [levelFilter, setLevelFilter] = useState('ALL')
  const [search, setSearch]   = useState('')
  const [tradeOnly, setTradeOnly] = useState(false)
  const [connected, setConnected] = useState(false)
  const bottomRef  = useRef(null)
  const containerRef = useRef(null)
  const wsRef      = useRef(null)
  const pendingRef = useRef([])  // buffer while we batch state updates

  // Flush pending entries into React state at most every 100ms
  useEffect(() => {
    const id = setInterval(() => {
      if (pendingRef.current.length === 0) return
      const batch = pendingRef.current.splice(0)
      setEntries(prev => {
        const next = [...prev, ...batch]
        // Keep last 2000 to avoid memory growth
        return next.length > 2000 ? next.slice(next.length - 2000) : next
      })
    }, 100)
    return () => clearInterval(id)
  }, [])

  // WebSocket
  useEffect(() => {
    let reconnectTimer = null
    let unmounted = false
    let delay = 2000

    function connect() {
      if (unmounted) return
      const ws = new WebSocket(WS_URL)
      wsRef.current = ws

      ws.onopen = () => { if (!unmounted) { setConnected(true); delay = 2000 } }
      ws.onmessage = (evt) => {
        try { pendingRef.current.push(JSON.parse(evt.data)) } catch {}
      }
      ws.onclose = () => {
        if (unmounted) return
        setConnected(false)
        reconnectTimer = setTimeout(() => { delay = Math.min(delay * 1.5, 30000); connect() }, delay)
      }
      ws.onerror = () => ws.close()
    }

    connect()
    return () => {
      unmounted = true
      clearTimeout(reconnectTimer)
      wsRef.current?.close()
    }
  }, [])

  // Auto-scroll when not paused
  useEffect(() => {
    if (!paused && bottomRef.current) {
      bottomRef.current.scrollIntoView({ behavior: 'smooth' })
    }
  }, [entries, paused])

  // Detect manual scroll-up to pause
  const onScroll = useCallback(() => {
    const el = containerRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 60
    setPaused(!atBottom)
  }, [])

  function scrollToBottom() {
    setPaused(false)
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }

  function clear() {
    setEntries([])
    pendingRef.current = []
  }

  // Filter
  const filtered = entries.filter(e => {
    if (levelFilter !== 'ALL' && e.level !== levelFilter) return false
    if (tradeOnly && !isTradeRelevant(e)) return false
    if (search) {
      const haystack = (e.msg + JSON.stringify(e.fields ?? {})).toLowerCase()
      if (!haystack.includes(search.toLowerCase())) return false
    }
    return true
  })

  const levelCounts = entries.reduce((acc, e) => {
    acc[e.level] = (acc[e.level] ?? 0) + 1
    return acc
  }, {})

  return (
    <div className="logs-page">
      {/* Toolbar */}
      <div className="logs-toolbar">
        <div className="logs-title-row">
          <h1 className="page-title">Logs</h1>
          <div className="sidebar-status" style={{ marginLeft: 12 }}>
            <span className={`dot-live ${connected ? 'on' : ''}`} />
            <span className="text2" style={{ fontSize: 12 }}>
              {connected ? 'streaming' : 'reconnecting…'}
            </span>
          </div>
        </div>

        <div className="logs-controls">
          {/* Level filter */}
          <div className="logs-level-btns">
            {['ALL', 'DEBUG', 'INFO', 'WARN', 'ERROR'].map(lvl => (
              <button
                key={lvl}
                className={`btn btn-secondary logs-level-btn ${levelFilter === lvl ? 'active' : ''}`}
                onClick={() => setLevelFilter(lvl)}
                style={lvl !== 'ALL' && LEVELS[lvl] ? {
                  '--level-color': LEVELS[lvl].color,
                } : {}}
              >
                {lvl === 'ALL' ? 'All' : lvl}
                {lvl !== 'ALL' && levelCounts[lvl] ? (
                  <span className="logs-count">{levelCounts[lvl]}</span>
                ) : null}
              </button>
            ))}
          </div>

          {/* Trade-only toggle */}
          <label className="logs-toggle-wrap">
            <label className="toggle" style={{ width: 34, height: 18 }}>
              <input type="checkbox" checked={tradeOnly} onChange={e => setTradeOnly(e.target.checked)} />
              <span className="toggle-slider" />
            </label>
            <span className="text2" style={{ fontSize: 12 }}>Trade signals only</span>
          </label>

          {/* Search */}
          <input
            type="text"
            className="form-input logs-search"
            placeholder="Search…"
            value={search}
            onChange={e => setSearch(e.target.value)}
          />

          <button className="btn btn-secondary" onClick={clear} style={{ fontSize: 12 }}>
            Clear
          </button>
        </div>
      </div>

      {/* Log area */}
      <div className="logs-container" ref={containerRef} onScroll={onScroll}>
        {filtered.length === 0 && (
          <div className="empty-state" style={{ padding: '60px 0' }}>
            <div className="empty-icon">📋</div>
            <div>{entries.length === 0 ? 'Waiting for logs…' : 'No entries match the filter'}</div>
            {entries.length === 0 && (
              <div className="text3" style={{ marginTop: 6 }}>
                Make sure the bot is running with <code className="mono">--web</code>
              </div>
            )}
          </div>
        )}

        {filtered.map((e, i) => {
          const lvl = LEVELS[e.level] ?? LEVELS.DEBUG
          const relevant = isTradeRelevant(e)
          return (
            <div
              key={i}
              className={`log-row ${relevant ? 'log-row-highlight' : ''}`}
              style={{ background: lvl.bg }}
            >
              <span className="log-ts mono">
                {new Date(e.ts).toLocaleTimeString('en-US', { hour12: false, fractionalSecondDigits: 3 })}
              </span>
              <span className="log-level mono" style={{ color: lvl.color }}>
                {lvl.short}
              </span>
              <span className="log-msg" style={{ color: lvl.color !== 'var(--text3)' ? lvl.color : 'var(--text)' }}>
                {e.msg}
              </span>
              <Fields fields={e.fields} />
            </div>
          )
        })}
        <div ref={bottomRef} />
      </div>

      {/* Paused banner */}
      {paused && (
        <button className="logs-paused-banner" onClick={scrollToBottom}>
          ▼ Scrolled up — click to resume live tail
        </button>
      )}

      {/* Stats bar */}
      <div className="logs-statusbar">
        <span className="text3">
          {filtered.length}/{entries.length} entries
          {search && ` matching "${search}"`}
        </span>
        {Object.entries(levelCounts).map(([lvl, n]) => (
          <span key={lvl} className="mono" style={{ color: LEVELS[lvl]?.color ?? 'var(--text2)', fontSize: 11 }}>
            {lvl}: {n}
          </span>
        ))}
      </div>
    </div>
  )
}
