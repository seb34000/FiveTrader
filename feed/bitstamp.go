package feed

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const bitstampWS = "wss://ws.bitstamp.net"

type bitstampMsg struct {
	Event   string          `json:"event"`
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

type bitstampTradeData struct {
	Timestamp string  `json:"timestamp"`
	Price     float64 `json:"price"`
	Amount    float64 `json:"amount"`
}

// RunBitstamp connects to Bitstamp live trades for the given channel
// (e.g. "live_trades_btcusd") and publishes ticks.
// Returns immediately without starting if channel is empty (asset not on Bitstamp).
func RunBitstamp(ctx context.Context, channel string, out chan<- Tick, log *zap.Logger) {
	if channel == "" {
		return
	}
	backoff := time.Second
	for {
		err := connectBitstamp(ctx, channel, out, log)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn("bitstamp feed error, reconnecting",
				zap.String("channel", channel), zap.Error(err), zap.Duration("backoff", backoff))
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

// connectBitstamp opens a single WebSocket connection to Bitstamp and reads live trade events.
func connectBitstamp(ctx context.Context, channel string, out chan<- Tick, log *zap.Logger) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, bitstampWS, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	sub := map[string]interface{}{
		"event": "bts:subscribe",
		"data":  map[string]string{"channel": channel},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	log.Info("bitstamp connected", zap.String("channel", channel))

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPingHandler(func(msg string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
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
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		var m bitstampMsg
		if err := json.Unmarshal(msg, &m); err != nil {
			continue
		}
		if m.Event != "trade" {
			continue
		}

		var data bitstampTradeData
		if err := json.Unmarshal(m.Data, &data); err != nil {
			continue
		}
		if data.Price <= 0 {
			continue
		}

		ts, err := strconv.ParseInt(data.Timestamp, 10, 64)
		if err != nil {
			ts = time.Now().Unix()
		}

		select {
		case out <- Tick{
			Source:    "bitstamp",
			Price:     data.Price,
			Volume:    data.Amount,
			Timestamp: time.Unix(ts, 0),
		}:
		default:
		}
	}
}
