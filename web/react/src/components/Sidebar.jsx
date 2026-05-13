import { NavLink } from 'react-router-dom'
import './Sidebar.css'

const NAV = [
  { to: '/',         icon: '⚡', label: 'Live'     },
  { to: '/logs',     icon: '🔍', label: 'Logs'     },
  { to: '/history',  icon: '📋', label: 'History'  },
  { to: '/settings', icon: '⚙️',  label: 'Settings' },
]

export default function Sidebar({ connected, mode }) {
  return (
    <aside className="sidebar">
      <div className="sidebar-logo">
        <span className="sidebar-logo-icon">📈</span>
        <span className="sidebar-logo-text">FiveTrader</span>
      </div>

      <nav className="sidebar-nav">
        {NAV.map(({ to, icon, label }) => (
          <NavLink
            key={to}
            to={to}
            end={to === '/'}
            className={({ isActive }) => `sidebar-link${isActive ? ' active' : ''}`}
          >
            <span className="sidebar-link-icon">{icon}</span>
            <span className="sidebar-link-label">{label}</span>
          </NavLink>
        ))}
      </nav>

      <div className="sidebar-footer">
        <div className="sidebar-status">
          <span className={`dot-live ${connected ? 'on' : ''}`} />
          <span>{connected ? 'Connected' : 'Disconnected'}</span>
        </div>
        {mode && (
          <span className={`badge ${modeBadge(mode)}`}>{mode}</span>
        )}
      </div>
    </aside>
  )
}

function modeBadge(mode) {
  switch (mode?.toUpperCase()) {
    case 'LIVE':    return 'badge-red'
    case 'SIM':     return 'badge-blue'
    case 'DRY-RUN': return 'badge-yellow'
    default:        return 'badge-yellow'
  }
}
