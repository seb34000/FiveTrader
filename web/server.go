package web

import (
	"bufio"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seb/fivetrader/sim"
)

// SessionMeta summarises one past trading session for the history page.
type SessionMeta struct {
	Name        string   `json:"name"`
	Date        string   `json:"date"`
	Time        string   `json:"time"`
	Mode        string   `json:"mode"`
	Assets      []string `json:"assets"`
	TotalTrades int      `json:"total_trades"`
	TotalPnL    float64  `json:"total_pnl"`
	WinCount    int      `json:"win_count"`
}

//go:embed static
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// Allow all origins for local dev dashboard
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server is the web dashboard HTTP server.
type Server struct {
	host    string // bind address, default "127.0.0.1"
	port    int
	hub     *Hub
	logHub  *LogHub
	workDir string // directory to scan for journal files
	user    string // Basic Auth username (empty = no auth)
	pass    string // Basic Auth password
}

// NewServer creates a new Server.
// host defaults to "127.0.0.1" if empty.
func NewServer(port int, hub *Hub, logHub *LogHub, workDir, host, user, pass string) *Server {
	if host == "" {
		host = "127.0.0.1"
	}
	return &Server{host: host, port: port, hub: hub, logHub: logHub, workDir: workDir, user: user, pass: pass}
}

// basicAuth wraps a handler with HTTP Basic Authentication when credentials are configured.
func (s *Server) basicAuth(next http.Handler) http.Handler {
	if s.user == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(s.user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="FiveTrader"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Static files — serve index.html at root
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("static fs: %w", err)
	}
	mux.Handle("/", s.basicAuth(http.FileServer(http.FS(staticFS))))

	// WebSocket endpoints (also protected when auth is set)
	mux.Handle("/ws",      s.basicAuth(http.HandlerFunc(s.handleWS)))
	mux.Handle("/ws/logs", s.basicAuth(http.HandlerFunc(s.handleLogsWS)))

	// API endpoints (all protected)
	mux.Handle("/api/journals",      s.basicAuth(http.HandlerFunc(s.handleJournals)))
	mux.Handle("/api/history",       s.basicAuth(http.HandlerFunc(s.handleHistory)))
	mux.Handle("/api/sessions",      s.basicAuth(http.HandlerFunc(s.handleSessions)))
	mux.Handle("/api/session-trades",s.basicAuth(http.HandlerFunc(s.handleSessionTrades)))
	mux.Handle("/api/config",        s.basicAuth(http.HandlerFunc(s.handleConfig)))

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", s.host, s.port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// handleLogsWS streams log entries to the browser.
// On connect: replays the ring buffer (last 500), then live-streams new entries.
func (s *Server) handleLogsWS(w http.ResponseWriter, r *http.Request) {
	if s.logHub == nil {
		http.Error(w, "log hub not configured", http.StatusServiceUnavailable)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, snapshot := s.logHub.Subscribe()
	defer s.logHub.Unsubscribe(ch)

	// Send replay
	for _, data := range snapshot {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}

	// Stream new entries
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleWS upgrades an HTTP request to a WebSocket connection.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := NewClient(s.hub, conn)
	go client.WritePump()
	go client.ReadPump()
}

// handleJournals returns a JSON list of available JSONL journal files.
func (s *Server) handleJournals(w http.ResponseWriter, r *http.Request) {
	pattern := filepath.Join(s.workDir, "*_journal_*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	sort.Strings(matches)
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = filepath.Base(m)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(names)
}

// setCORSHeaders adds permissive CORS headers for local dashboard use.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// handleSessions returns metadata for all sessions found in sessions/ subdirectory.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	sessDir := filepath.Join(s.workDir, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		w.Write([]byte("[]"))
		return
	}

	var sessions []SessionMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// name format: 20260402_001732_dry-run  or  20260403_014256_sim
		parts := strings.SplitN(name, "_", 3)
		if len(parts) < 3 {
			continue
		}
		meta := SessionMeta{
			Name: name,
			Date: parts[0][:4] + "-" + parts[0][4:6] + "-" + parts[0][6:],
			Time: parts[1][:2] + ":" + parts[1][2:4] + ":" + parts[1][4:],
			Mode: parts[2],
		}

		// Scan JSONL files to collect assets and aggregate stats
		sessionPath := filepath.Join(sessDir, name)
		files, _ := filepath.Glob(filepath.Join(sessionPath, "*.jsonl"))
		for _, f := range files {
			base := filepath.Base(f)
			// Derive asset name: sim_btc.jsonl → btc, trades_eth.jsonl → eth
			assetName := strings.TrimSuffix(base, ".jsonl")
			assetName = strings.TrimPrefix(assetName, "sim_")
			assetName = strings.TrimPrefix(assetName, "trades_")
			meta.Assets = append(meta.Assets, assetName)

			// Aggregate P&L and trade count
			fh, err := os.Open(f)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(fh)
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}
				var rec sim.TradeRecord
				if json.Unmarshal(line, &rec) == nil {
					meta.TotalTrades++
					meta.TotalPnL += rec.PnL
					if rec.Won {
						meta.WinCount++
					}
				}
			}
			fh.Close()
		}
		sessions = append(sessions, meta)
	}

	// Newest first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Name > sessions[j].Name
	})

	json.NewEncoder(w).Encode(sessions)
}

// handleSessionTrades returns trades for a specific session and optional asset filter.
// Query: ?session=20260403_014256_sim&asset=btc  (asset optional)
func (s *Server) handleSessionTrades(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	sessionName := r.URL.Query().Get("session")
	assetFilter := r.URL.Query().Get("asset")

	if sessionName == "" || strings.ContainsAny(sessionName, "/\\") {
		http.Error(w, "missing or invalid session param", http.StatusBadRequest)
		return
	}

	sessionPath := filepath.Join(s.workDir, "sessions", sessionName)
	files, _ := filepath.Glob(filepath.Join(sessionPath, "*.jsonl"))

	var records []sim.TradeRecord
	for _, f := range files {
		base := filepath.Base(f)
		// Filter by asset if requested
		if assetFilter != "" && !strings.Contains(base, assetFilter) {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(fh)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var rec sim.TradeRecord
			if json.Unmarshal(line, &rec) == nil {
				records = append(records, rec)
			}
		}
		fh.Close()
	}

	// Sort by entry time ascending
	sort.Slice(records, func(i, j int) bool {
		return records[i].EntryTime.Before(records[j].EntryTime)
	})

	w.Header().Set("Content-Type", "application/json")
	if records == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(records)
}

// handleConfig handles GET and POST for /api/config (reads/writes .env).
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	envPath := filepath.Join(s.workDir, ".env")

	switch r.Method {
	case http.MethodGet:
		vals, _ := readEnvFile(envPath)
		if vals == nil {
			vals = map[string]string{}
		}
		// Mask sensitive fields before sending to browser
		if pk, ok := vals["PRIVATE_KEY"]; ok && len(pk) > 6 {
			vals["PRIVATE_KEY"] = pk[:6] + strings.Repeat("*", 58)
		}
		for _, key := range []string{"POLY_API_KEY", "POLY_API_SECRET", "POLY_API_PASSPHRASE"} {
			if v := vals[key]; v != "" {
				if len(v) > 4 {
					vals[key] = v[:4] + strings.Repeat("*", len(v)-4)
				} else {
					vals[key] = "****"
				}
			}
		}
		json.NewEncoder(w).Encode(vals)

	case http.MethodPost:
		var updates map[string]string
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		current, _ := readEnvFile(envPath)
		if current == nil {
			current = map[string]string{}
		}
		for k, v := range updates {
			// Skip masked values sent back unchanged
			if strings.Contains(v, "****") {
				continue
			}
			current[k] = v
		}
		if err := writeEnvFile(envPath, current); err != nil {
			http.Error(w, fmt.Sprintf("write failed: %v", err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// readEnvFile parses a .env file into a key→value map, preserving order via a slice.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vals := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		vals[line[:idx]] = line[idx+1:]
	}
	return vals, scanner.Err()
}

// writeEnvFile writes key=value pairs back to the .env file.
// It reads the original file first to preserve comments and ordering.
func writeEnvFile(path string, updates map[string]string) error {
	// Read original to preserve comments and line order
	original, _ := os.ReadFile(path)
	lines := strings.Split(string(original), "\n")

	written := map[string]bool{}
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx < 0 {
			out = append(out, line)
			continue
		}
		key := trimmed[:idx]
		if val, ok := updates[key]; ok {
			out = append(out, key+"="+val)
			written[key] = true
		} else {
			out = append(out, line)
		}
	}
	// Append any new keys not present in original
	for k, v := range updates {
		if !written[k] {
			out = append(out, k+"="+v)
		}
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600)
}

// handleHistory returns a JSON array of sim.TradeRecord from a JSONL journal.
// Query params:
//
//	?file=sim_journal_20240101_120000.jsonl   (filename, no path)
//	?limit=200                                (default 200, 0 = all)
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("file")
	if filename == "" || strings.ContainsAny(filename, "/\\") {
		http.Error(w, "missing or invalid file param", http.StatusBadRequest)
		return
	}
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	path := filepath.Join(s.workDir, filename)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	var records []sim.TradeRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec sim.TradeRecord
		if err := json.Unmarshal(line, &rec); err == nil {
			records = append(records, rec)
		}
	}

	// Keep only last N records
	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}

	w.Header().Set("Content-Type", "application/json")
	if records == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(records)
}
