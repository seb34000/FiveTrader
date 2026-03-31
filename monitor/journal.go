package monitor

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// TradeEntry is written to the journal on trade open.
// In SIM mode this is managed by sim.Simulator; in LIVE mode it is written here.
type TradeEntry struct {
	ID         string    `json:"id"`
	Strategy   string    `json:"strategy"`
	Direction  string    `json:"direction"`
	TokenID    string    `json:"token_id"`
	TokenPrice float64   `json:"token_price"`
	USDCStaked float64   `json:"usdc_staked"`
	WindowEnd  time.Time `json:"window_end"`
	EntryTime  time.Time `json:"entry_time"`
}

// Journal writes trade entries as newline-delimited JSON (JSONL) for persistence.
// In LIVE mode, this survives restarts and lets operators audit open positions.
type Journal struct {
	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
	path    string
}

// NewJournal opens (or creates) a JSONL journal at path.
func NewJournal(path string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Journal{file: f, encoder: json.NewEncoder(f), path: path}, nil
}

// Record appends a trade entry. Thread-safe; best-effort (errors are silently dropped).
func (j *Journal) Record(e TradeEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.encoder.Encode(e)
}

// Path returns the journal file path.
func (j *Journal) Path() string { return j.path }

// Close flushes and closes the journal file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.file.Close()
}
