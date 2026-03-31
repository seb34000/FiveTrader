package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type binanceTrade struct {
	EventType string `json:"e"`
	Price     string `json:"p"`
	Quantity  string `json:"q"`
	TradeTime int64  `json:"T"`
}

// RunBinance connects to Binance aggTrade stream for the given pair (e.g. "btcusdt")
// and publishes ticks. Reconnects with exponential backoff on disconnection.
func RunBinance(ctx context.Context, pair string, out chan<- Tick, log *zap.Logger) {
	wsURL := fmt.Sprintf("wss://stream.binance.com:9443/ws/%s@aggTrade", pair)
	backoff := time.Second
	for {
		err := connectBinance(ctx, wsURL, pair, out, log)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn("binance feed error, reconnecting",
				zap.String("pair", pair), zap.Error(err), zap.Duration("backoff", backoff))
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

// connectBinance opens a single WebSocket connection and reads aggTrade events.
// Returns when the connection closes or ctx is cancelled.
func connectBinance(ctx context.Context, wsURL, pair string, out chan<- Tick, log *zap.Logger) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Info("binance connected", zap.String("pair", pair))

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		return nil
	})
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

		var trade binanceTrade
		if err := json.Unmarshal(msg, &trade); err != nil || trade.EventType != "aggTrade" {
			continue
		}
		price, err := strconv.ParseFloat(trade.Price, 64)
		if err != nil || price <= 0 {
			continue
		}
		vol, _ := strconv.ParseFloat(trade.Quantity, 64)

		select {
		case out <- Tick{
			Source:    "binance",
			Price:     price,
			Volume:    vol,
			Timestamp: time.UnixMilli(trade.TradeTime),
		}:
		default:
		}
	}
}
