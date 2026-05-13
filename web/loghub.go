package web

import (
	"encoding/json"
	"sync"
	"time"
)

const ringSize = 500 // entries kept in memory for late-connecting clients

// LogEntry is a single log line sent to the browser.
type LogEntry struct {
	TS     time.Time      `json:"ts"`
	Level  string         `json:"level"`
	Msg    string         `json:"msg"`
	Fields map[string]any `json:"fields,omitempty"`
}

// LogHub receives log entries from the custom zapcore and broadcasts them to
// all connected /ws/logs WebSocket clients. Thread-safe.
type LogHub struct {
	mu      sync.Mutex
	ring    [ringSize]LogEntry
	head    int // index of next write slot
	count   int // total written (capped at ringSize for reads)
	clients map[chan []byte]struct{}
}

// NewLogHub allocates a new LogHub.
func NewLogHub() *LogHub {
	return &LogHub{clients: make(map[chan []byte]struct{})}
}

// Publish records one entry and broadcasts it to connected clients.
func (h *LogHub) Publish(e LogEntry) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}

	h.mu.Lock()
	h.ring[h.head] = e
	h.head = (h.head + 1) % ringSize
	if h.count < ringSize {
		h.count++
	}
	// snapshot clients while holding the lock
	clients := make([]chan []byte, 0, len(h.clients))
	for ch := range h.clients {
		clients = append(clients, ch)
	}
	h.mu.Unlock()

	for _, ch := range clients {
		select {
		case ch <- data:
		default: // client too slow — drop rather than block
		}
	}
}

// Subscribe returns a channel that will receive new entries, and the current
// snapshot (oldest→newest) for replay. Call Unsubscribe when done.
func (h *LogHub) Subscribe() (ch chan []byte, snapshot [][]byte) {
	ch = make(chan []byte, 256)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	// Build replay slice: ring entries oldest-first
	snap := make([]LogEntry, h.count)
	for i := 0; i < h.count; i++ {
		idx := (h.head - h.count + i + ringSize) % ringSize
		snap[i] = h.ring[idx]
	}
	h.mu.Unlock()

	for _, e := range snap {
		if data, err := json.Marshal(e); err == nil {
			snapshot = append(snapshot, data)
		}
	}
	return
}

// Unsubscribe removes and closes the channel.
func (h *LogHub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}
