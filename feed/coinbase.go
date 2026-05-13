package feed

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const coinbaseWS = "wss://ws-feed.exchange.coinbase.com"

type coinbaseSubMsg struct {
	Type       string   `json:"type"`
	ProductIDs []string `json:"product_ids"`
	Channels   []string `json:"channels"`
}

type coinbaseTicker struct {
	Type      string `json:"type"`
	ProductID string `json:"product_id"`
	Price     string `json:"price"`
	LastSize  string `json:"last_size"`
	Time      string `json:"time"`
}

// RunCoinbase connects to the Coinbase Exchange WebSocket for the given product (e.g. "BTC-USD")
// and publishes ticks.
func RunCoinbase(ctx context.Context, productID string, out chan<- Tick, log *zap.Logger) {
	backoff := time.Second
	for {
		err := connectCoinbase(ctx, productID, out, log)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn("coinbase feed error, reconnecting",
				zap.String("product", productID), zap.Error(err), zap.Duration("backoff", backoff))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		if err == nil {
			backoff = time.Second
		}
	}
}

// connectCoinbase opens a single WebSocket connection to Coinbase Exchange and reads ticker events.
func connectCoinbase(ctx context.Context, productID string, out chan<- Tick, log *zap.Logger) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, coinbaseWS, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()

	sub := coinbaseSubMsg{
		Type:       "subscribe",
		ProductIDs: []string{productID},
		Channels:   []string{"ticker"},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	log.Debug("coinbase connected", zap.String("product", productID))

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPingHandler(func(msg string) error {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(msg), time.Now().Add(5*time.Second))
	})

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		var tick coinbaseTicker
		if err := json.Unmarshal(msg, &tick); err != nil || tick.Type != "ticker" {
			continue
		}
		if tick.ProductID != productID {
			continue
		}
		price, err := strconv.ParseFloat(tick.Price, 64)
		if err != nil || price <= 0 {
			continue
		}
		size, _ := strconv.ParseFloat(tick.LastSize, 64)
		ts, err := time.Parse(time.RFC3339Nano, tick.Time)
		if err != nil || ts.IsZero() {
			ts = time.Now()
		}
		select {
		case out <- Tick{
			Source:    "coinbase",
			Price:     price,
			Volume:    size,
			Timestamp: ts,
		}:
		default:
		}
	}
}
