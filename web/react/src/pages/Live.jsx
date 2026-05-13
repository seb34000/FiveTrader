import { useState, useMemo } from 'react'
import {
  LineChart, Line, XAxis, YAxis, Tooltip, ResponsiveContainer, ReferenceLine
} from 'recharts'
import './Live.css'

// ── Helpers ──────────────────────────────────────────────────────────────────

function fmt$(n) {
  if (n == null) return '—'
  const abs = Math.abs(n)
  const s = abs >= 100 ? abs.toFixed(2) : abs >= 10 ? abs.toFixed(2) : abs.toFixed(2)
  return (n < 0 ? '-$' : '+$') + s
}

function fmtPrice(n) {
  if (!n) return '—'
  return '$' + n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

function fmtPct(n) {
  if (n == null) return '—'
  return (n >= 0 ? '+' : '') + (n * 100).toFixed(3) + '%'
}

function fmtWin(n) {
  if (n == null) return '—'
  return (n * 100).toFixed(1) + '%'
}

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

// Countdown: seconds remaining in current window
function useWindowCountdown(windowEnd) {
  const [now, setNow] = useState(Date.now)
  useState(() => {
    const id = setInterval(() => setNow(Date.now()), 500)
    return () => clearInterval(id)
  })
  if (!windowEnd) return null
  const secs = Math.max(0, Math.round((new Date(windowEnd) - now) / 1000))
  return secs
}

// ── Sub-components ────────────────────────────────────────────────────────────

function GlobalStats({ data }) {
  const pnl = data?.total_pnl ?? 0
  const trades = data?.total_trades ?? 0
  const winRate = data?.total_win_rate ?? 0
  const dailyLoss = data?.total_daily_loss ?? 0

  return (
    <div className="stat-grid">
      <div className="stat-card">
        <div className="stat-label">Total P&L</div>
        <div className="stat-value" style={{ color: pnlColor(pnl) }}>
          {pnl >= 0 ? '+' : ''}{pnl.toFixed(2)}$
        </div>
        <div className="stat-sub">current session</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">Trades</div>
        <div className="stat-value">{trades}</div>
        <div className="stat-sub">settled</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">Win Rate</div>
        <div className="stat-value" style={{ color: winRate > 0.55 ? 'var(--green)' : 'var(--text2)' }}>
          {(winRate * 100).toFixed(1)}%
        </div>
        <div className="stat-sub">across all assets</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">Daily Loss</div>
        <div className="stat-value" style={{ color: dailyLoss > 100 ? 'var(--red)' : 'var(--text)' }}>
          ${dailyLoss.toFixed(2)}
        </div>
        <div className="stat-sub">max $200 circuit breaker</div>
      </div>
    </div>
  )
}

function WindowCountdown({ windowEnd, windowStart }) {
  const [now, setNow] = useState(() => Date.now())
  useState(() => {
    const id = setInterval(() => setNow(Date.now()), 500)
    return () => clearInterval(id)
  })

  if (!windowEnd) return <div className="countdown-block">—</div>

  const end = new Date(windowEnd).getTime()
  const start = new Date(windowStart).getTime()
  const secs = Math.max(0, Math.round((end - now) / 1000))
  const total = (end - start) / 1000
  const pct = Math.max(0, Math.min(100, (secs / total) * 100))

  let color = 'var(--green)'
  if (secs < 30) color = 'var(--yellow)'
  if (secs < 8)  color = 'var(--red)'

  return (
    <div className="countdown-block">
      <div className="countdown-num mono" style={{ color }}>{secs}s</div>
      <div className="countdown-bar-wrap">
        <div className="countdown-bar" style={{ width: pct + '%', background: color }} />
      </div>
      <div className="countdown-label text3">window closes</div>
    </div>
  )
}

function PriceFeeds({ asset }) {
  const prices = [
    { label: 'Binance',  val: asset.price_binance },
    { label: 'Bitstamp', val: asset.price_bitstamp },
    { label: 'Coinbase', val: asset.price_coinbase },
    { label: 'Oracle',   val: asset.oracle_price, dim: true },
  ]

  return (
    <div className="price-feeds">
      {prices.map(({ label, val, dim }) => (
        <div key={label} className="price-feed-row">
          <span className="price-feed-label text2">{label}</span>
          <span className={`price-feed-val mono ${dim ? 'text2' : ''}`}>
            {fmtPrice(val)}
          </span>
        </div>
      ))}
    </div>
  )
}

function OracleDelta({ delta, ageSec }) {
  // delta is already a percentage (backend: (live-oracle)/oracle * 100)
  const pct = delta ?? 0
  const isLag = Math.abs(pct) > 0.3
  const color = pct > 0 ? 'var(--green)' : pct < 0 ? 'var(--red)' : 'var(--text2)'

  return (
    <div className="oracle-delta-block">
      <div className="oracle-delta-val mono" style={{ color }}>
        {pct >= 0 ? '+' : ''}{pct.toFixed(4)}%
      </div>
      <div className="oracle-delta-label text2">oracle lag</div>
      <div className="oracle-age text3">
        {ageSec != null ? `${ageSec.toFixed(0)}s ago` : '—'}
      </div>
      {isLag && (
        <div className={`badge ${pct > 0 ? 'badge-green' : 'badge-red'}`} style={{ marginTop: 6 }}>
          {pct > 0 ? '▲ UP signal' : '▼ DOWN signal'}
        </div>
      )}
    </div>
  )
}

// ── Signal Conditions panel ───────────────────────────────────────────────────

function CondRow({ label, value, ok, detail }) {
  return (
    <div className="cond-row">
      <span className={`cond-dot ${ok ? 'ok' : 'no'}`} />
      <span className="cond-label text2">{label}</span>
      <span className={`cond-value mono ${ok ? 'green' : 'red'}`}>{value}</span>
      {detail && <span className="cond-detail text3">{detail}</span>}
    </div>
  )
}

function SignalConditions({ asset }) {
  const [now, setNow] = useState(() => Date.now())
  useState(() => {
    const id = setInterval(() => setNow(Date.now()), 500)
    return () => clearInterval(id)
  })

  const delta      = asset.oracle_delta ?? 0   // already %
  const ageSec     = asset.oracle_age_sec ?? 0
  const askUp      = asset.ask_up ?? 0
  const askDown    = asset.ask_down ?? 0
  const livePrice  = asset.live_price ?? 0
  const windowOpen = asset.window_open ?? 0
  const windowEnd  = asset.window_end ? new Date(asset.window_end).getTime() : 0
  const windowStart = asset.window_start ? new Date(asset.window_start).getTime() : 0

  const secsRemaining = windowEnd > 0 ? Math.max(0, (windowEnd - now) / 1000) : 0
  const windowElapsed = windowStart > 0 ? Math.max(0, (now - windowStart) / 1000) : 0
  const windowDeltaPct = windowOpen > 0 ? (livePrice - windowOpen) / windowOpen * 100 : 0
  const hedgeSum = askUp + askDown

  // Oracle Lag conditions
  const olagDir      = delta > 0 ? 'UP' : 'DOWN'
  const olagAsk      = delta > 0 ? askUp : askDown
  const olagDelta_ok = Math.abs(delta) > 0.3
  const olagAge_ok   = ageSec >= 3 && ageSec <= 120
  const olagTime_ok  = secsRemaining > 5
  const olagPrice_ok = olagAsk > 0 && olagAsk < 0.92
  const olagReady    = olagDelta_ok && olagAge_ok && olagTime_ok && olagPrice_ok

  // Window Delta conditions
  const wdDir      = windowDeltaPct > 0 ? 'UP' : 'DOWN'
  const wdAsk      = windowDeltaPct > 0 ? askUp : askDown
  const wdWindow_ok = windowElapsed >= 270 && windowElapsed <= 292
  const wdDelta_ok  = Math.abs(windowDeltaPct) > 0.1
  const wdPrice_ok  = wdAsk > 0 && wdAsk < 0.72
  const wdReady     = wdWindow_ok && wdDelta_ok && wdPrice_ok

  // Dump Hedge conditions
  const dhArb_ok   = hedgeSum > 0 && hedgeSum < 0.96
  const dhEdge     = hedgeSum > 0 ? ((1 / hedgeSum - 1) * 100) : 0
  const dhReady    = dhArb_ok

  return (
    <div className="card section-gap">
      <div className="card-title">Signal Conditions</div>
      <div className="cond-grid">

        {/* Oracle Lag */}
        <div className={`cond-strat ${olagReady ? 'cond-strat-ready' : ''}`}>
          <div className="cond-strat-header">
            <span className={`badge ${olagReady ? 'badge-green' : 'badge-blue'}`}>
              {olagReady ? '🔥 ' : ''}Oracle Lag
            </span>
            {olagReady && (
              <span className="mono" style={{ fontSize: 12, color: 'var(--green)', marginLeft: 8 }}>
                BUY {olagDir} @ ${olagAsk.toFixed(3)}
              </span>
            )}
          </div>
          <CondRow
            label="Delta > 0.3%"
            value={`${delta >= 0 ? '+' : ''}${delta.toFixed(4)}%`}
            ok={olagDelta_ok}
            detail={`need >${delta > 0 ? '' : '-'}0.3% — ${(Math.abs(delta) / 0.3 * 100).toFixed(0)}%`}
          />
          <CondRow
            label="Oracle age 3–120s"
            value={`${ageSec.toFixed(0)}s`}
            ok={olagAge_ok}
            detail={ageSec < 3 ? 'too fresh' : ageSec > 120 ? 'too stale' : 'ok'}
          />
          <CondRow
            label="Time left > 5s"
            value={`${secsRemaining.toFixed(0)}s`}
            ok={olagTime_ok}
          />
          <CondRow
            label="Token price < $0.92"
            value={`$${olagAsk.toFixed(3)}`}
            ok={olagPrice_ok}
            detail={`→ payout $${olagAsk > 0 ? (1/olagAsk).toFixed(3) : '—'} (+${olagAsk > 0 ? ((1/olagAsk-1)*100).toFixed(1) : '—'}%)`}
          />
        </div>

        {/* Window Delta */}
        <div className={`cond-strat ${wdReady ? 'cond-strat-ready' : ''}`}>
          <div className="cond-strat-header">
            <span className={`badge ${wdReady ? 'badge-green' : 'badge-purple'}`}>
              {wdReady ? '🔥 ' : ''}Window Delta
            </span>
            {wdReady && (
              <span className="mono" style={{ fontSize: 12, color: 'var(--green)', marginLeft: 8 }}>
                BUY {wdDir} @ ${wdAsk.toFixed(3)}
              </span>
            )}
          </div>
          <CondRow
            label="T-30s window (270–292s)"
            value={`T-${secsRemaining.toFixed(0)}s`}
            ok={wdWindow_ok}
            detail={windowElapsed < 270 ? `wait ${(270 - windowElapsed).toFixed(0)}s` : windowElapsed > 292 ? 'too late' : 'in window!'}
          />
          <CondRow
            label="Δ from open > 0.1%"
            value={`${windowDeltaPct >= 0 ? '+' : ''}${windowDeltaPct.toFixed(4)}%`}
            ok={wdDelta_ok}
            detail={`open $${windowOpen > 0 ? windowOpen.toFixed(0) : '—'}`}
          />
          <CondRow
            label="Token price < $0.72"
            value={`$${wdAsk.toFixed(3)}`}
            ok={wdPrice_ok}
            detail={`→ payout $${wdAsk > 0 ? (1/wdAsk).toFixed(3) : '—'} (+${wdAsk > 0 ? ((1/wdAsk-1)*100).toFixed(1) : '—'}%)`}
          />
        </div>

        {/* Dump Hedge */}
        <div className={`cond-strat ${dhReady ? 'cond-strat-ready' : ''}`}>
          <div className="cond-strat-header">
            <span className={`badge ${dhReady ? 'badge-green' : 'badge-cyan'}`}>
              {dhReady ? '🔥 ' : ''}Dump Hedge
            </span>
            {dhReady && (
              <span className="mono" style={{ fontSize: 12, color: 'var(--green)', marginLeft: 8 }}>
                BUY BOTH +{dhEdge.toFixed(2)}% guaranteed
              </span>
            )}
          </div>
          <CondRow
            label="UP + DOWN < $0.96"
            value={`$${hedgeSum.toFixed(4)}`}
            ok={dhArb_ok}
            detail={dhArb_ok
              ? `arb edge = +${dhEdge.toFixed(3)}%`
              : `missing $${(hedgeSum - 0.96).toFixed(4)} for arb`}
          />
          <CondRow
            label="UP ask"
            value={`$${askUp.toFixed(3)}`}
            ok={askUp > 0}
            detail={`payout $${askUp > 0 ? (1/askUp).toFixed(3) : '—'}`}
          />
          <CondRow
            label="DOWN ask"
            value={`$${askDown.toFixed(3)}`}
            ok={askDown > 0}
            detail={`payout $${askDown > 0 ? (1/askDown).toFixed(3) : '—'}`}
          />
        </div>

      </div>
    </div>
  )
}

function MarketPrices({ askUp, askDown }) {
  return (
    <div className="market-prices">
      <div className="market-price-row">
        <div className="market-price-side up">UP</div>
        <div className="market-price-ask mono">
          ${(askUp ?? 0).toFixed(3)}
        </div>
        <div className="market-price-payout text2 mono">
          → ${askUp > 0 ? (1 / askUp).toFixed(2) : '—'}
        </div>
      </div>
      <div className="market-price-divider text3 mono">+</div>
      <div className="market-price-row">
        <div className="market-price-side down">DOWN</div>
        <div className="market-price-ask mono">
          ${(askDown ?? 0).toFixed(3)}
        </div>
        <div className="market-price-payout text2 mono">
          → ${askDown > 0 ? (1 / askDown).toFixed(2) : '—'}
        </div>
      </div>
      <div className="market-price-sum text3">
        sum: <span className="mono">{((askUp ?? 0) + (askDown ?? 0)).toFixed(3)}</span>
        {(askUp + askDown) < 0.96 && (
          <span className="badge badge-cyan" style={{ marginLeft: 8 }}>arb!</span>
        )}
      </div>
    </div>
  )
}

function TradeRow({ trade, settled }) {
  const pnl = trade.pnl ?? 0
  return (
    <tr>
      <td>
        <span className={`badge ${stratBadge(trade.strategy)}`}>
          {trade.strategy?.replace('_', ' ')}
        </span>
      </td>
      <td>
        <span className={`badge ${trade.direction === 'UP' ? 'badge-green' : 'badge-red'}`}>
          {trade.direction}
        </span>
      </td>
      <td className="mono">${(trade.usdc_staked ?? 0).toFixed(2)}</td>
      <td className="mono">${(trade.token_price ?? 0).toFixed(3)}</td>
      {settled && (
        <td className="mono" style={{ color: pnlColor(pnl) }}>
          {pnl >= 0 ? '+' : ''}{pnl.toFixed(2)}$
        </td>
      )}
      <td className="text2 mono" style={{ fontSize: 11 }}>
        {new Date(trade.timestamp).toLocaleTimeString()}
      </td>
    </tr>
  )
}

// ── Main page ────────────────────────────────────────────────────────────────

export default function Live({ data, connected }) {
  const assets = data?.assets ?? {}
  const symbols = Object.keys(assets).sort()
  const [activeTab, setActiveTab] = useState(null)
  const active = activeTab ?? symbols[0]

  const asset = assets[active] ?? null

  // Build per-asset P&L history for mini chart (recent_trades)
  const pnlHistory = useMemo(() => {
    if (!asset?.recent_trades?.length) return []
    let cum = 0
    return asset.recent_trades.map((t, i) => {
      cum += t.pnl ?? 0
      return { i, cum: +cum.toFixed(2) }
    })
  }, [asset?.recent_trades])

  if (!connected && !data) {
    return (
      <div className="empty-state" style={{ minHeight: '60vh' }}>
        <div className="empty-icon">📡</div>
        <div>Connecting to bot…</div>
        <div className="text3">Make sure the bot is running with <code className="mono">--web</code></div>
      </div>
    )
  }

  return (
    <div>
      {/* Header */}
      <div className="page-header">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <h1 className="page-title">Live Dashboard</h1>
          {data?.mode && (
            <span className={`badge ${data.mode === 'LIVE' ? 'badge-red' : data.mode === 'SIM' ? 'badge-blue' : 'badge-yellow'}`}>
              {data.mode}
            </span>
          )}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {data?.address && (
            <span className="mono text2" style={{ fontSize: 12 }}>
              {data.address.slice(0, 6)}…{data.address.slice(-4)}
            </span>
          )}
          <div className="sidebar-status">
            <span className={`dot-live ${connected ? 'on' : ''}`} />
            <span className="text2" style={{ fontSize: 12 }}>
              {connected ? 'live' : 'reconnecting…'}
            </span>
          </div>
        </div>
      </div>

      {/* Global stats */}
      <GlobalStats data={data} />

      {/* Asset tabs */}
      {symbols.length > 0 && (
        <>
          <div className="tabs">
            {symbols.map(sym => {
              const a = assets[sym]
              return (
                <button
                  key={sym}
                  className={`tab-btn ${active === sym ? 'active' : ''}`}
                  onClick={() => setActiveTab(sym)}
                >
                  <span style={{ fontWeight: 700 }}>{sym.toUpperCase()}</span>
                  {a?.live_price > 0 && (
                    <span className="text2 mono" style={{ fontSize: 11, marginLeft: 6 }}>
                      {fmtPrice(a.live_price)}
                    </span>
                  )}
                </button>
              )
            })}
          </div>

          {asset && <AssetPanel asset={asset} pnlHistory={pnlHistory} />}
        </>
      )}

      {symbols.length === 0 && (
        <div className="empty-state">
          <div className="empty-icon">⏳</div>
          <div>Waiting for data…</div>
        </div>
      )}
    </div>
  )
}

function AssetPanel({ asset, pnlHistory }) {
  return (
    <div>
      {/* Top row: feeds | oracle | window | market */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 160px 200px', gap: 16, marginBottom: 16 }}>
        <div className="card">
          <div className="card-title">Price Feeds</div>
          <PriceFeeds asset={asset} />
        </div>
        <div className="card">
          <div className="card-title">Oracle Delta</div>
          <OracleDelta delta={asset.oracle_delta} ageSec={asset.oracle_age_sec} />
        </div>
        <div className="card" style={{ textAlign: 'center' }}>
          <div className="card-title">Window</div>
          <WindowCountdown windowEnd={asset.window_end} windowStart={asset.window_start} />
        </div>
        <div className="card">
          <div className="card-title">Market Prices</div>
          <MarketPrices askUp={asset.ask_up} askDown={asset.ask_down} />
        </div>
      </div>

      {/* Signal conditions */}
      <SignalConditions asset={asset} />

      {/* P&L + Signal */}
      <div className="grid2 section-gap">
        <div className="card">
          <div className="card-title">
            Session P&L
            <span className="mono" style={{ color: pnlColor(asset.pnl), marginLeft: 'auto', fontSize: 13 }}>
              {fmt$(asset.pnl)}
            </span>
          </div>
          {pnlHistory.length > 1 ? (
            <ResponsiveContainer width="100%" height={120}>
              <LineChart data={pnlHistory} margin={{ top: 4, right: 4, left: 0, bottom: 0 }}>
                <XAxis dataKey="i" hide />
                <YAxis hide domain={['auto', 'auto']} />
                <Tooltip
                  contentStyle={{ background: 'var(--bg3)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}
                  formatter={(v) => [`${v >= 0 ? '+' : ''}$${v.toFixed(2)}`, 'Cum. P&L']}
                  labelFormatter={() => ''}
                />
                <ReferenceLine y={0} stroke="var(--border2)" strokeDasharray="3 3" />
                <Line
                  type="monotone"
                  dataKey="cum"
                  stroke={asset.pnl >= 0 ? 'var(--green)' : 'var(--red)'}
                  dot={false}
                  strokeWidth={2}
                />
              </LineChart>
            </ResponsiveContainer>
          ) : (
            <div className="empty-state" style={{ padding: '30px 0' }}>
              <div className="text3">No settled trades yet</div>
            </div>
          )}
          <div style={{ display: 'flex', gap: 16, marginTop: 10 }}>
            <div>
              <div className="stat-label">Trades</div>
              <div className="mono" style={{ fontSize: 18, fontWeight: 700 }}>{asset.settled_trades ?? 0}</div>
            </div>
            <div>
              <div className="stat-label">Win Rate</div>
              <div className="mono" style={{ fontSize: 18, fontWeight: 700, color: (asset.win_rate ?? 0) > 0.55 ? 'var(--green)' : 'var(--text)' }}>
                {fmtWin(asset.win_rate)}
              </div>
            </div>
            <div>
              <div className="stat-label">Daily Loss</div>
              <div className="mono" style={{ fontSize: 18, fontWeight: 700 }}>${(asset.daily_loss ?? 0).toFixed(2)}</div>
            </div>
          </div>
        </div>

        <div className="card">
          <div className="card-title">Last Signal</div>
          {asset.last_signal ? (
            <div className="mono" style={{ fontSize: 13, color: 'var(--cyan)', lineHeight: 1.6 }}>
              {asset.last_signal}
            </div>
          ) : (
            <div className="text3">No signal fired yet</div>
          )}

          {/* Open trades */}
          {asset.open_trades?.length > 0 && (
            <>
              <div className="card-title" style={{ marginTop: 16 }}>Open Positions</div>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Strategy</th>
                      <th>Dir</th>
                      <th>Staked</th>
                      <th>Price</th>
                      <th>Time</th>
                    </tr>
                  </thead>
                  <tbody>
                    {asset.open_trades.map(t => <TradeRow key={t.id} trade={t} settled={false} />)}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </div>
      </div>

      {/* Recent trades */}
      {asset.recent_trades?.length > 0 && (
        <div className="card section-gap">
          <div className="card-title">Recent Trades</div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Strategy</th>
                  <th>Dir</th>
                  <th>Staked</th>
                  <th>Price</th>
                  <th>P&L</th>
                  <th>Time</th>
                </tr>
              </thead>
              <tbody>
                {[...asset.recent_trades].reverse().map(t => (
                  <TradeRow key={t.id} trade={t} settled={true} />
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
