package web

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/seb/fivetrader/risk"
	"github.com/seb/fivetrader/ui"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 32768
	sendBufSize    = 64
	throttleInterval = 500 * time.Millisecond
)

// TradeJSON is a JSON-friendly version of risk.Trade.
type TradeJSON struct {
	ID         string    `json:"id"`
	Strategy   string    `json:"strategy"`
	Direction  string    `json:"direction"`
	TokenID    string    `json:"token_id"`
	USDCStaked float64   `json:"usdc_staked"`
	TokenPrice float64   `json:"token_price"`
	Timestamp  time.Time `json:"timestamp"`
	WindowEnd  time.Time `json:"window_end"`
	Settled    bool      `json:"settled"`
	PnL        float64   `json:"pnl"`
}

// AssetStateJSON is the JSON representation of ui.AssetState.
type AssetStateJSON struct {
	Symbol        string      `json:"symbol"`
	Name          string      `json:"name"`
	LivePrice     float64     `json:"live_price"`
	OraclePrice   float64     `json:"oracle_price"`
	OracleDelta   float64     `json:"oracle_delta"`
	OracleAgeSec  float64     `json:"oracle_age_sec"`
	PriceBinance  float64     `json:"price_binance"`
	PriceBitstamp float64     `json:"price_bitstamp"`
	PriceCoinbase float64     `json:"price_coinbase"`
	WindowStart   time.Time   `json:"window_start"`
	WindowEnd     time.Time   `json:"window_end"`
	WindowOpen    float64     `json:"window_open"`
	AskUp         float64     `json:"ask_up"`
	AskDown       float64     `json:"ask_down"`
	SettledTrades int         `json:"settled_trades"`
	PnL           float64     `json:"pnl"`
	WinRate       float64     `json:"win_rate"`
	DailyLoss     float64     `json:"daily_loss"`
	OpenTrades    []TradeJSON `json:"open_trades"`
	RecentTrades  []TradeJSON `json:"recent_trades"`
	LastSignal    string      `json:"last_signal"`
}

// UpdateJSON is the full JSON payload sent to browser clients.
type UpdateJSON struct {
	Mode           string                    `json:"mode"`
	Address        string                    `json:"address"`
	TotalPnL       float64                   `json:"total_pnl"`
	TotalTrades    int                       `json:"total_trades"`
	TotalWinRate   float64                   `json:"total_win_rate"`
	TotalDailyLoss float64                   `json:"total_daily_loss"`
	Assets         map[string]AssetStateJSON `json:"assets"`
	ServerTime     time.Time                 `json:"server_time"`
}

func tradesToJSON(trades []risk.Trade) []TradeJSON {
	out := make([]TradeJSON, len(trades))
	for i, t := range trades {
		out[i] = TradeJSON{
			ID:         t.ID,
			Strategy:   t.Strategy,
			Direction:  t.Direction,
			TokenID:    t.TokenID,
			USDCStaked: t.USDCStaked,
			TokenPrice: t.TokenPrice,
			Timestamp:  t.Timestamp,
			WindowEnd:  t.WindowEnd,
			Settled:    t.Settled,
			PnL:        t.PnL,
		}
	}
	return out
}

// toUpdateJSON converts a ui.Update to UpdateJSON.
func toUpdateJSON(u ui.Update) UpdateJSON {
	assets := make(map[string]AssetStateJSON, len(u.Assets))
	for sym, a := range u.Assets {
		assets[sym] = AssetStateJSON{
			Symbol:        a.Symbol,
			Name:          a.Name,
			LivePrice:     a.LivePrice,
			OraclePrice:   a.OraclePrice,
			OracleDelta:   a.OracleDelta,
			OracleAgeSec:  a.OracleAge.Seconds(),
			PriceBinance:  a.PriceBinance,
			PriceBitstamp: a.PriceBitstamp,
			PriceCoinbase: a.PriceCoinbase,
			WindowStart:   a.WindowStart,
			WindowEnd:     a.WindowEnd,
			WindowOpen:    a.WindowOpen,
			AskUp:         a.AskUp,
			AskDown:       a.AskDown,
			SettledTrades: a.SettledTrades,
			PnL:           a.PnL,
			WinRate:       a.WinRate,
			DailyLoss:     a.DailyLoss,
			OpenTrades:    tradesToJSON(a.OpenTrades),
			RecentTrades:  tradesToJSON(a.RecentTrades),
			LastSignal:    a.LastSignal,
		}
	}
	return UpdateJSON{
		Mode:           u.Mode,
		Address:        u.Address,
		TotalPnL:       u.TotalPnL,
		TotalTrades:    u.TotalTrades,
		TotalWinRate:   u.TotalWinRate,
		TotalDailyLoss: u.TotalDailyLoss,
		Assets:         assets,
		ServerTime:     time.Now(),
	}
}

// Hub maintains the set of active WebSocket clients and broadcasts messages.
type Hub struct {
	clients    map[*Client]struct{}
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	pending    ui.Update
	hasPending bool
	mu         sync.Mutex
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		broadcast:  make(chan []byte, 16),
		register:   make(chan *Client, 4),
		unregister: make(chan *Client, 4),
	}
}

// Push enqueues a new update. Called from the main goroutine.
func (h *Hub) Push(u ui.Update) {
	h.mu.Lock()
	h.pending = u
	h.hasPending = true
	h.mu.Unlock()
}

// Run starts the hub event loop. Must be called in a goroutine.
func (h *Hub) Run() {
	ticker := time.NewTicker(throttleInterval)
	defer ticker.Stop()

	var lastMsg []byte

	for {
		select {
		case client := <-h.register:
			h.clients[client] = struct{}{}
			if lastMsg != nil {
				select {
				case client.send <- lastMsg:
				default:
				}
			}

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}

		case msg := <-h.broadcast:
			lastMsg = msg
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}

		case <-ticker.C:
			h.mu.Lock()
			if !h.hasPending {
				h.mu.Unlock()
				continue
			}
			u := h.pending
			h.hasPending = false
			h.mu.Unlock()

			data, err := json.Marshal(toUpdateJSON(u))
			if err != nil {
				continue
			}
			select {
			case h.broadcast <- data:
			default:
			}
		}
	}
}

// Client is a websocket connection middleman between the hub and the browser.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// NewClient creates and registers a new client with the hub.
func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	c := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, sendBufSize),
	}
	hub.register <- c
	return c
}

// ReadPump handles incoming messages and detects disconnection.
func (c *Client) ReadPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// WritePump pumps messages from the hub to the WebSocket connection.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
