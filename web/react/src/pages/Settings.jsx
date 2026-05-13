import { useState, useEffect } from 'react'

// Fields definition — controls rendering order and display
const FIELDS = [
  // Wallet
  { section: 'Wallet', key: 'PRIVATE_KEY', label: 'Private Key', type: 'password', hint: 'Hex key with 0x prefix. Masked after save.' },

  // Polymarket
  { section: 'Polymarket API', key: 'POLY_API_KEY',        label: 'API Key',        type: 'password' },
  { section: 'Polymarket API', key: 'POLY_API_SECRET',     label: 'API Secret',     type: 'password' },
  { section: 'Polymarket API', key: 'POLY_API_PASSPHRASE', label: 'Passphrase',     type: 'password' },

  // Network
  { section: 'Network', key: 'POLYGON_RPC', label: 'Polygon RPC', type: 'text', hint: 'Alchemy recommended: polygon-mainnet.g.alchemy.com/v2/YOUR_KEY' },

  // Risk
  { section: 'Risk', key: 'MAX_BET_USDC',       label: 'Max Bet (USDC)',    type: 'number', hint: 'Maximum USDC per trade. Default: 50' },
  { section: 'Risk', key: 'MAX_DAILY_LOSS_USDC', label: 'Max Daily Loss',    type: 'number', hint: 'Circuit breaker. Bot stops when reached. Default: 200' },
  { section: 'Risk', key: 'MAX_CONCURRENT_BETS', label: 'Max Open Trades',   type: 'number', hint: 'Simultaneous open positions. Default: 3' },
  { section: 'Risk', key: 'KELLY_FRACTION',       label: 'Kelly Fraction',    type: 'number', hint: 'Fraction of full Kelly (0.25 = quarter-Kelly). Default: 0.25' },
]

const TOGGLES = [
  { key: 'ENABLE_ORACLE_LAG',     label: 'Oracle Lag Arbitrage',  desc: 'Strategy 1 — exploits Chainlink latency (70–90% win rate)' },
  { key: 'ENABLE_WINDOW_DELTA',   label: 'Window Delta T-30s',    desc: 'Strategy 2 — momentum at T-30s before close (55–62%)' },
  { key: 'ENABLE_DUMP_HEDGE',     label: 'Dump & Hedge Arbitrage', desc: 'Strategy 3 — risk-free arb when UP+DOWN < $0.96 (100%)' },
  { key: 'ENABLE_DUMP_HEDGE_LIVE', label: 'Dump Hedge Live Mode', desc: 'Allow dump_hedge to place real orders (disabled by default — two-leg risk)' },
]

function isBoolStr(v) {
  return v === 'true' || v === 'false'
}

export default function Settings() {
  const [values, setValues] = useState({})
  const [original, setOriginal] = useState({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [status, setStatus] = useState(null) // { type: 'success'|'error', msg }
  const [showRaw, setShowRaw] = useState(false)

  useEffect(() => {
    fetch('/api/config')
      .then(r => r.json())
      .then(data => {
        setValues(data ?? {})
        setOriginal(data ?? {})
        setLoading(false)
      })
      .catch(() => {
        setStatus({ type: 'error', msg: 'Failed to load config. Is the bot running with --web?' })
        setLoading(false)
      })
  }, [])

  function set(key, val) {
    setValues(prev => ({ ...prev, [key]: val }))
  }

  function setToggle(key, checked) {
    setValues(prev => ({ ...prev, [key]: checked ? 'true' : 'false' }))
  }

  function isDirty(key) {
    return values[key] !== original[key]
  }

  async function save() {
    setSaving(true)
    setStatus(null)
    try {
      const res = await fetch('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(values),
      })
      if (!res.ok) throw new Error(await res.text())
      setStatus({ type: 'success', msg: 'Saved to .env — restart the bot for changes to take effect.' })
      // Reload to get masked values back
      const fresh = await fetch('/api/config').then(r => r.json())
      setValues(fresh)
      setOriginal(fresh)
    } catch (e) {
      setStatus({ type: 'error', msg: `Save failed: ${e.message}` })
    } finally {
      setSaving(false)
    }
  }

  const hasChanges = Object.keys(values).some(k => isDirty(k))

  if (loading) {
    return <div className="empty-state" style={{ minHeight: '60vh' }}><div className="text3">Loading config…</div></div>
  }

  // Group fields by section
  const sections = {}
  for (const f of FIELDS) {
    if (!sections[f.section]) sections[f.section] = []
    sections[f.section].push(f)
  }

  return (
    <div style={{ maxWidth: 720 }}>
      <div className="page-header">
        <h1 className="page-title">Settings</h1>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <button
            className="btn btn-secondary"
            onClick={() => setShowRaw(v => !v)}
            style={{ fontSize: 12 }}
          >
            {showRaw ? 'Form view' : 'Raw .env'}
          </button>
          <button
            className="btn btn-primary"
            onClick={save}
            disabled={saving || !hasChanges}
          >
            {saving ? 'Saving…' : 'Save changes'}
          </button>
        </div>
      </div>

      {status && (
        <div
          className={`card section-gap`}
          style={{
            borderColor: status.type === 'success' ? 'var(--green2)' : 'var(--red2)',
            background: status.type === 'success' ? 'rgba(35,134,54,.1)' : 'rgba(218,54,51,.1)',
            color: status.type === 'success' ? 'var(--green)' : 'var(--red)',
            fontSize: 13,
          }}
        >
          {status.msg}
        </div>
      )}

      <div className="card section-gap" style={{ background: 'rgba(210,153,34,.08)', borderColor: 'rgba(210,153,34,.3)' }}>
        <div style={{ fontSize: 12, color: 'var(--yellow)', lineHeight: 1.6 }}>
          <strong>Note:</strong> Changes are written to <code className="mono">.env</code>.
          The running bot will <strong>not</strong> pick them up until you restart it.
          Sensitive values (private key, API secrets) are masked after loading.
        </div>
      </div>

      {showRaw ? (
        <div className="card">
          <div className="card-title">Raw .env preview</div>
          <pre className="mono" style={{ fontSize: 12, color: 'var(--text2)', lineHeight: 1.8, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
            {Object.entries(values).map(([k, v]) => `${k}=${v}`).join('\n')}
          </pre>
        </div>
      ) : (
        <>
          {Object.entries(sections).map(([sectionName, fields]) => (
            <div key={sectionName} className="card section-gap">
              <h2 style={{ marginBottom: 16, color: 'var(--text)', borderBottom: '1px solid var(--border)', paddingBottom: 10 }}>
                {sectionName}
              </h2>
              {fields.map(f => (
                <div key={f.key} className="form-group">
                  <label className="form-label" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    {f.label}
                    {isDirty(f.key) && (
                      <span className="badge badge-yellow" style={{ fontSize: 10 }}>modified</span>
                    )}
                  </label>
                  <input
                    type={f.type === 'password' ? 'text' : f.type}
                    className="form-input"
                    value={values[f.key] ?? ''}
                    onChange={e => set(f.key, e.target.value)}
                    placeholder={f.hint}
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {f.hint && <div className="form-hint">{f.hint}</div>}
                </div>
              ))}
            </div>
          ))}

          {/* Strategy toggles */}
          <div className="card section-gap">
            <h2 style={{ marginBottom: 16, color: 'var(--text)', borderBottom: '1px solid var(--border)', paddingBottom: 10 }}>
              Strategies
            </h2>
            {TOGGLES.map(t => (
              <div key={t.key} className="toggle-row">
                <div className="toggle-info">
                  <div className="toggle-name" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    {t.label}
                    {isDirty(t.key) && (
                      <span className="badge badge-yellow" style={{ fontSize: 10 }}>modified</span>
                    )}
                  </div>
                  <div className="toggle-desc">{t.desc}</div>
                </div>
                <label className="toggle">
                  <input
                    type="checkbox"
                    checked={values[t.key] === 'true'}
                    onChange={e => setToggle(t.key, e.target.checked)}
                  />
                  <span className="toggle-slider" />
                </label>
              </div>
            ))}
          </div>

          {/* Mode */}
          <div className="card section-gap">
            <h2 style={{ marginBottom: 16, color: 'var(--text)', borderBottom: '1px solid var(--border)', paddingBottom: 10 }}>
              Mode
            </h2>
            <div className="form-group">
              <label className="form-label">
                Dry Run
                {isDirty('DRY_RUN') && (
                  <span className="badge badge-yellow" style={{ fontSize: 10, marginLeft: 8 }}>modified</span>
                )}
              </label>
              <div style={{ display: 'flex', gap: 8 }}>
                {['true', 'false'].map(v => (
                  <button
                    key={v}
                    className={`btn ${values['DRY_RUN'] === v ? 'btn-primary' : 'btn-secondary'}`}
                    onClick={() => set('DRY_RUN', v)}
                  >
                    {v === 'true' ? '🔒 Dry Run (safe)' : '⚡ Live Ready'}
                  </button>
                ))}
              </div>
              <div className="form-hint">
                DRY_RUN=true → no orders placed. Set to false + SIM_MODE=false to enable live trading.
              </div>
            </div>
          </div>
        </>
      )}

      {hasChanges && (
        <div style={{ position: 'sticky', bottom: 0, background: 'var(--bg)', paddingTop: 12, paddingBottom: 12, borderTop: '1px solid var(--border)', display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button className="btn btn-secondary" onClick={() => { setValues(original); setStatus(null) }}>
            Discard changes
          </button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>
            {saving ? 'Saving…' : '💾 Save to .env'}
          </button>
        </div>
      )}
    </div>
  )
}
