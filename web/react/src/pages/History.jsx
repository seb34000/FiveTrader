import { useState, useEffect, useMemo } from 'react'
import {
  LineChart, Line, XAxis, YAxis, Tooltip, ResponsiveContainer,
  BarChart, Bar, Cell, ReferenceLine
} from 'recharts'

// ── Helpers ───────────────────────────────────────────────────────────────────

function pnlColor(n) {
  if (n > 0) return 'var(--green)'
  if (n < 0) return 'var(--red)'
  return 'var(--text2)'
}

function stratBadge(s) {
  switch (s) {
    case 'oracle_lag':   return 'badge-blue'
    case 'window_delta': return 'badge-purple'
    case 'dump_hedge':   return 'badge-cyan'
    default:             return 'badge-yellow'
  }
}

function formatSessionName(s) {
  if (!s) return ''
  // 20260403_014256_sim → 2026-04-03 01:42:56 [SIM]
  const parts = s.split('_')
  if (parts.length < 3) return s
  const d = parts[0]
  const t = parts[1]
  const mode = parts.slice(2).join('-').toUpperCase()
  return `${d.slice(0,4)}-${d.slice(4,6)}-${d.slice(6)} ${t.slice(0,2)}:${t.slice(2,4)}:${t.slice(4)} [${mode}]`
}

// ── Main ──────────────────────────────────────────────────────────────────────

export default function History() {
  const [sessions, setSessions] = useState([])
  const [selectedSession, setSelectedSession] = useState(null)
  const [selectedAsset, setSelectedAsset] = useState('all')
  const [trades, setTrades] = useState([])
  const [loading, setLoading] = useState(false)
  const [sortKey, setSortKey] = useState('entry_time')
  const [sortDir, setSortDir] = useState('desc')
  const [stratFilter, setStratFilter] = useState('all')

  // Load session list
  useEffect(() => {
    fetch('/api/sessions')
      .then(r => r.json())
      .then(data => {
        setSessions(data ?? [])
        if (data?.length) setSelectedSession(data[0].name)
      })
      .catch(() => {})
  }, [])

  // Load trades when session/asset changes
  useEffect(() => {
    if (!selectedSession) return
    setLoading(true)
    const asset = selectedAsset !== 'all' ? `&asset=${selectedAsset}` : ''
    fetch(`/api/session-trades?session=${selectedSession}${asset}`)
      .then(r => r.json())
      .then(data => { setTrades(data ?? []); setLoading(false) })
      .catch(() => setLoading(false))
  }, [selectedSession, selectedAsset])

  const session = sessions.find(s => s.name === selectedSession)

  // Cumulative P&L for chart
  const pnlCurve = useMemo(() => {
    let cum = 0
    return trades.map(t => {
      cum += t.pnl ?? 0
      return {
        time: new Date(t.entry_time).toLocaleTimeString(),
        cum: +cum.toFixed(2),
        pnl: +(t.pnl ?? 0).toFixed(2),
        won: t.won,
      }
    })
  }, [trades])

  // Strategy breakdown
  const stratBreakdown = useMemo(() => {
    const map = {}
    for (const t of trades) {
      const s = t.strategy ?? 'unknown'
      if (!map[s]) map[s] = { strategy: s, count: 0, wins: 0, pnl: 0 }
      map[s].count++
      if (t.won) map[s].wins++
      map[s].pnl += t.pnl ?? 0
    }
    return Object.values(map).sort((a, b) => b.count - a.count)
  }, [trades])

  // Stats
  const stats = useMemo(() => {
    if (!trades.length) return null
    const wins = trades.filter(t => t.won).length
    const totalPnL = trades.reduce((s, t) => s + (t.pnl ?? 0), 0)
    const avgStake = trades.reduce((s, t) => s + (t.usdc_staked ?? 0), 0) / trades.length
    return { wins, totalPnL, winRate: wins / trades.length, avgStake, count: trades.length }
  }, [trades])

  // Filtered + sorted trades for table
  const filtered = useMemo(() => {
    let list = stratFilter !== 'all' ? trades.filter(t => t.strategy === stratFilter) : [...trades]
    list.sort((a, b) => {
      let va = a[sortKey], vb = b[sortKey]
      if (typeof va === 'string') va = va.toLowerCase()
      if (typeof vb === 'string') vb = vb.toLowerCase()
      if (va < vb) return sortDir === 'asc' ? -1 : 1
      if (va > vb) return sortDir === 'asc' ? 1 : -1
      return 0
    })
    return list
  }, [trades, stratFilter, sortKey, sortDir])

  function toggleSort(key) {
    if (sortKey === key) setSortDir(d => d === 'asc' ? 'desc' : 'asc')
    else { setSortKey(key); setSortDir('desc') }
  }

  function SortTh({ col, label }) {
    const active = sortKey === col
    return (
      <th onClick={() => toggleSort(col)} style={{ cursor: 'pointer', userSelect: 'none' }}>
        {label} {active ? (sortDir === 'asc' ? '↑' : '↓') : ''}
      </th>
    )
  }

  const sessionAssets = session?.assets ?? []

  return (
    <div>
      <div className="page-header">
        <h1 className="page-title">Trade History</h1>
      </div>

      {/* Session + asset selector */}
      <div className="card section-gap" style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
        <div className="form-group" style={{ margin: 0, flex: '1 1 280px' }}>
          <label className="form-label">Session</label>
          <select
            className="form-input"
            value={selectedSession ?? ''}
            onChange={e => { setSelectedSession(e.target.value); setSelectedAsset('all') }}
          >
            {sessions.map(s => (
              <option key={s.name} value={s.name}>{formatSessionName(s.name)}</option>
            ))}
          </select>
        </div>
        <div className="form-group" style={{ margin: 0, flex: '0 0 120px' }}>
          <label className="form-label">Asset</label>
          <select
            className="form-input"
            value={selectedAsset}
            onChange={e => setSelectedAsset(e.target.value)}
          >
            <option value="all">All</option>
            {sessionAssets.map(a => (
              <option key={a} value={a}>{a.toUpperCase()}</option>
            ))}
          </select>
        </div>
        <div className="form-group" style={{ margin: 0, flex: '0 0 140px' }}>
          <label className="form-label">Strategy</label>
          <select
            className="form-input"
            value={stratFilter}
            onChange={e => setStratFilter(e.target.value)}
          >
            <option value="all">All</option>
            <option value="oracle_lag">Oracle Lag</option>
            <option value="window_delta">Window Delta</option>
            <option value="dump_hedge">Dump Hedge</option>
          </select>
        </div>
      </div>

      {/* Session summary banner */}
      {session && (
        <div className="stat-grid section-gap">
          <div className="stat-card">
            <div className="stat-label">Trades</div>
            <div className="stat-value">{stats?.count ?? session.total_trades}</div>
            <div className="stat-sub">{session.mode?.toUpperCase()}</div>
          </div>
          <div className="stat-card">
            <div className="stat-label">Total P&L</div>
            <div className="stat-value" style={{ color: pnlColor(stats?.totalPnL ?? session.total_pnl) }}>
              {(stats?.totalPnL ?? session.total_pnl) >= 0 ? '+' : ''}
              ${(stats?.totalPnL ?? session.total_pnl).toFixed(2)}
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-label">Win Rate</div>
            <div className="stat-value" style={{ color: (stats?.winRate ?? 0) > 0.55 ? 'var(--green)' : 'var(--text)' }}>
              {stats ? (stats.winRate * 100).toFixed(1) + '%' : '—'}
            </div>
            <div className="stat-sub">{stats?.wins ?? session.win_count} wins</div>
          </div>
          <div className="stat-card">
            <div className="stat-label">Avg Stake</div>
            <div className="stat-value">${(stats?.avgStake ?? 0).toFixed(2)}</div>
            <div className="stat-sub">USDC / trade</div>
          </div>
        </div>
      )}

      {loading && (
        <div className="empty-state"><div className="text3">Loading…</div></div>
      )}

      {!loading && trades.length === 0 && (
        <div className="empty-state">
          <div className="empty-icon">📭</div>
          <div>No trades in this session</div>
        </div>
      )}

      {!loading && trades.length > 0 && (
        <>
          {/* Charts row */}
          <div className="grid2 section-gap">
            <div className="card">
              <div className="card-title">Cumulative P&L</div>
              <ResponsiveContainer width="100%" height={180}>
                <LineChart data={pnlCurve} margin={{ top: 4, right: 8, left: 0, bottom: 0 }}>
                  <XAxis dataKey="time" hide />
                  <YAxis
                    width={50}
                    tickFormatter={v => `$${v}`}
                    tick={{ fontSize: 11, fill: 'var(--text2)' }}
                  />
                  <Tooltip
                    contentStyle={{ background: 'var(--bg3)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}
                    formatter={(v) => [`${v >= 0 ? '+' : ''}$${v.toFixed(2)}`, 'Cum. P&L']}
                  />
                  <ReferenceLine y={0} stroke="var(--border2)" strokeDasharray="4 2" />
                  <Line
                    type="monotone"
                    dataKey="cum"
                    stroke={stats?.totalPnL >= 0 ? 'var(--green)' : 'var(--red)'}
                    dot={false}
                    strokeWidth={2}
                  />
                </LineChart>
              </ResponsiveContainer>
            </div>

            <div className="card">
              <div className="card-title">P&L per Trade</div>
              <ResponsiveContainer width="100%" height={180}>
                <BarChart data={pnlCurve} margin={{ top: 4, right: 8, left: 0, bottom: 0 }}>
                  <XAxis dataKey="time" hide />
                  <YAxis
                    width={50}
                    tickFormatter={v => `$${v}`}
                    tick={{ fontSize: 11, fill: 'var(--text2)' }}
                  />
                  <Tooltip
                    contentStyle={{ background: 'var(--bg3)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}
                    formatter={(v) => [`${v >= 0 ? '+' : ''}$${v.toFixed(2)}`, 'P&L']}
                  />
                  <ReferenceLine y={0} stroke="var(--border2)" />
                  <Bar dataKey="pnl" radius={[2, 2, 0, 0]}>
                    {pnlCurve.map((entry, idx) => (
                      <Cell key={idx} fill={entry.won ? 'var(--green)' : 'var(--red)'} />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>

          {/* Strategy breakdown */}
          {stratBreakdown.length > 1 && (
            <div className="card section-gap">
              <div className="card-title">Strategy Breakdown</div>
              <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
                {stratBreakdown.map(s => (
                  <div key={s.strategy} className="card" style={{ flex: '1 1 160px', background: 'var(--bg3)' }}>
                    <div style={{ marginBottom: 8 }}>
                      <span className={`badge ${stratBadge(s.strategy)}`}>
                        {s.strategy.replace('_', ' ')}
                      </span>
                    </div>
                    <div className="mono" style={{ fontSize: 18, fontWeight: 700, color: pnlColor(s.pnl) }}>
                      {s.pnl >= 0 ? '+' : ''}{s.pnl.toFixed(2)}$
                    </div>
                    <div className="text2" style={{ fontSize: 12, marginTop: 4 }}>
                      {s.wins}/{s.count} wins ({s.count > 0 ? ((s.wins / s.count) * 100).toFixed(0) : 0}%)
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Trade table */}
          <div className="card">
            <div className="card-title">
              Trades
              <span className="badge badge-blue" style={{ marginLeft: 'auto' }}>{filtered.length}</span>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <SortTh col="strategy" label="Strategy" />
                    <SortTh col="direction" label="Dir" />
                    <SortTh col="usdc_staked" label="Staked" />
                    <SortTh col="token_price" label="Token $" />
                    <th>Win Prob</th>
                    <th>Edge</th>
                    <SortTh col="pnl" label="P&L" />
                    <th>BTC Open</th>
                    <th>BTC Settle</th>
                    <SortTh col="entry_time" label="Entry" />
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((t, i) => (
                    <tr key={t.id ?? i}>
                      <td>
                        <span className={`badge ${stratBadge(t.strategy)}`}>
                          {(t.strategy ?? '').replace('_', ' ')}
                        </span>
                      </td>
                      <td>
                        <span className={`badge ${t.direction === 'UP' ? 'badge-green' : 'badge-red'}`}>
                          {t.direction}
                        </span>
                      </td>
                      <td className="mono">${(t.usdc_staked ?? 0).toFixed(2)}</td>
                      <td className="mono">${(t.token_price ?? 0).toFixed(3)}</td>
                      <td className="mono text2">{t.win_prob ? (t.win_prob * 100).toFixed(0) + '%' : '—'}</td>
                      <td className="mono text2">{t.edge ? (t.edge * 100).toFixed(1) + '%' : '—'}</td>
                      <td className="mono" style={{ color: pnlColor(t.pnl), fontWeight: 600 }}>
                        {(t.pnl ?? 0) >= 0 ? '+' : ''}{(t.pnl ?? 0).toFixed(2)}$
                      </td>
                      <td className="mono text2">${(t.window_open_btc ?? 0).toLocaleString('en-US', { maximumFractionDigits: 0 })}</td>
                      <td className="mono text2">${(t.settle_btc ?? 0).toLocaleString('en-US', { maximumFractionDigits: 0 })}</td>
                      <td className="text2 mono" style={{ fontSize: 11 }}>
                        {t.entry_time ? new Date(t.entry_time).toLocaleTimeString() : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
