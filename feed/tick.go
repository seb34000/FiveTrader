package feed

import "time"

// Tick is a single trade event from any price source.
type Tick struct {
	Source    string
	Price     float64
	Volume    float64
	Timestamp time.Time
}
