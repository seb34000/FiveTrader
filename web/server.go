package web

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/seb/fivetrader/sim"
)

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
	port       int
	hub        *Hub
	workDir    string // directory to scan for journal files
}

// NewServer creates a new Server.
func NewServer(port int, hub *Hub, workDir string) *Server {
	return &Server{port: port, hub: hub, workDir: workDir}
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Static files — serve index.html at root
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("static fs: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.handleWS)

	// API endpoints
	mux.HandleFunc("/api/journals", s.handleJournals)
	mux.HandleFunc("/api/history", s.handleHistory)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
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
