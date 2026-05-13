import { Routes, Route } from 'react-router-dom'
import Sidebar from './components/Sidebar.jsx'
import Live from './pages/Live.jsx'
import Logs from './pages/Logs.jsx'
import History from './pages/History.jsx'
import Settings from './pages/Settings.jsx'
import { useWebSocket } from './hooks/useWebSocket.js'

export default function App() {
  const { data, connected } = useWebSocket()

  return (
    <div className="layout">
      <Sidebar connected={connected} mode={data?.mode} />
      <main className="main-content">
        <Routes>
          <Route path="/"         element={<Live data={data} connected={connected} />} />
          <Route path="/logs"     element={<Logs />} />
          <Route path="/history"  element={<History />} />
          <Route path="/settings" element={<Settings />} />
        </Routes>
      </main>
    </div>
  )
}
