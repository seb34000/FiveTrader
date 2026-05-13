package strategy

import (
	"time"

	"github.com/seb/fivetrader/market"
)

// Direction of the bet.
type Direction int

const (
	DirectionAbstain Direction = 0
	DirectionUp      Direction = 1
	DirectionDown    Direction = -1
)

// String returns the human-readable name of the direction ("UP", "DOWN", or "BOTH").
func (d Direction) String() string {
	switch d {
	case DirectionUp:
		return "UP"
	case DirectionDown:
		return "DOWN"
	default:
		return "BOTH"
	}
}

// Signal is a trading signal produced by a strategy.
type Signal struct {
	Strategy     string
	Direction    Direction
	TokenID      string    // which token to buy; "BOTH" for dump_hedge
	TokenIDDown  string    // second token for dump_hedge (Direction==DirectionAbstain)
	AskPrice     float64   // current ask price of the token [0,1]
	AskPriceDown float64   // ask price of DOWN token (dump_hedge only)
	WinProb      float64   // estimated win probability [0,1]
	Edge         float64   // WinProb - AskPrice
	Confidence   float64   // signal confidence [0,1]
	NegRisk      bool      // true if market uses NegRisk CTF exchange
	FeeRateBps   int64     // taker fee rate in bps — must match EIP-712 signature
	GeneratedAt  time.Time
}

// Context is the snapshot of market data available to strategies.
type Context struct {
	LivePrice   float64
	OraclePrice float64
	OracleAge   time.Duration // how old is the oracle price
	Market      market.State
	WindowOpen  float64   // BTC price at window start
	Now         time.Time
}

// SecondsElapsed returns seconds elapsed in the current 5min window.
func (c *Context) SecondsElapsed() float64 {
	return c.Now.Sub(c.Market.WindowStart).Seconds()
}

// SecondsRemaining returns seconds left in the current 5min window.
func (c *Context) SecondsRemaining() float64 {
	return c.Market.WindowEnd.Sub(c.Now).Seconds()
}

// Strategy evaluates a signal given current market context.
type Strategy interface {
	Name() string
	Evaluate(ctx *Context) *Signal
}
