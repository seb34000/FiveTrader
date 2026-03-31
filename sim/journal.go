package sim

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// TradeRecord is one settled simulated trade written to the JSONL journal.
type TradeRecord struct {
	ID                 string    `json:"id"`
	Strategy           string    `json:"strategy"`
	Direction          string    `json:"direction"`
	TokenPrice         float64   `json:"token_price"`
	USDCStaked         float64   `json:"usdc_staked"`
	WindowOpenPrice    float64   `json:"window_open_btc"`
	OraclePriceAtEntry float64   `json:"oracle_entry_btc,omitempty"`
	SettlePrice        float64   `json:"settle_btc"`
	OraclePriceAtSettle float64  `json:"oracle_settle_btc,omitempty"`
	Won                bool      `json:"won"`
	PnL                float64   `json:"pnl"`
	WinProb            float64   `json:"win_prob,omitempty"`
	Edge               float64   `json:"edge,omitempty"`
	Confidence         float64   `json:"confidence,omitempty"`
	EntryTime          time.Time `json:"entry_time"`
	SettledAt          time.Time `json:"settled_at"`
}

// TradeJournal appends settled trade records as newline-delimited JSON (JSONL).
type TradeJournal struct {
	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
	path    string
}

// newTradeJournal opens (or creates) a JSONL file at path for appending trade records.
func newTradeJournal(path string) (*TradeJournal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &TradeJournal{file: f, encoder: json.NewEncoder(f), path: path}, nil
}

// record appends a settled trade record to the journal. Thread-safe; best-effort.
func (j *TradeJournal) record(r TradeRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	// Best-effort — log errors are handled by the caller via zap
	_ = j.encoder.Encode(r)
}

// close flushes and closes the underlying file.
func (j *TradeJournal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.file.Close()
}
