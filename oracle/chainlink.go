package oracle

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"go.uber.org/zap"
)

const aggregatorABI = `[{
	"inputs": [],
	"name": "latestRoundData",
	"outputs": [
		{"name": "roundId",         "type": "uint80"},
		{"name": "answer",          "type": "int256"},
		{"name": "startedAt",       "type": "uint256"},
		{"name": "updatedAt",       "type": "uint256"},
		{"name": "answeredInRound", "type": "uint80"}
	],
	"stateMutability": "view",
	"type": "function"
}]`

// fallbackRPCs are tried in order when the primary RPC is rate-limited or unavailable.
// All are truly keyless public endpoints — no account or API key required.
var fallbackRPCs = []string{
	"https://polygon-rpc.com",       // official Polygon public RPC
	"https://polygon.drpc.org",      // dRPC public — no auth
	"https://1rpc.io/matic",         // 1RPC — privacy-first, no auth
	"https://rpc-mainnet.maticvigil.com", // MaticVigil public
}

// sessionEnd describes why a polling session terminated.
type sessionEnd int

const (
	sessionEndCancelled  sessionEnd = iota // ctx cancelled — stop entirely
	sessionEndDisconnect                   // transient error — retry same RPC
	sessionEndRateLimit                    // 429 or capacity exceeded — rotate to next RPC
)

// Price is a Chainlink oracle price update.
type Price struct {
	Value     float64
	RoundID   uint64
	UpdatedAt time.Time
}

// maskRPCURL replaces the API key in Alchemy/Infura-style RPC URLs with "****"
// to prevent credential leaks in logs. Masks the last path segment if long enough.
func maskRPCURL(rpcURL string) string {
	idx := strings.LastIndex(rpcURL, "/")
	if idx < 0 || idx == len(rpcURL)-1 {
		return rpcURL
	}
	key := rpcURL[idx+1:]
	if len(key) <= 6 {
		return rpcURL
	}
	return rpcURL[:idx+1] + key[:4] + "****"
}

// shouldRotate returns true if the error means the current RPC is permanently unusable
// and we should immediately switch to the next one without waiting for 3 retries.
func shouldRotate(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "Too Many Requests") ||
		strings.Contains(s, "capacity limit") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "Rate limit") ||
		strings.Contains(s, "-32005") || // JSON-RPC limit exceeded (Infura / Alchemy)
		strings.Contains(s, "Unauthorized") || // endpoint requires API key (Ankr, etc.)
		strings.Contains(s, "API key") || // generic auth-required message
		strings.Contains(s, "no such host") || // DNS failure — wrong/dead domain
		strings.Contains(s, "connection refused") // port closed
}

// buildRPCList builds the ordered list of RPCs to try: primary first, then fallbacks
// (deduplicating any public URLs that match the primary).
func buildRPCList(primaryRPC string) []string {
	rpcs := []string{primaryRPC}
	for _, fb := range fallbackRPCs {
		if fb != primaryRPC {
			rpcs = append(rpcs, fb)
		}
	}
	return rpcs
}

// RunPoller polls a Chainlink aggregator at contractAddr and publishes prices on out.
// It automatically rotates through public fallback RPCs on 429 / rate-limit errors,
// so the oracle stays alive even when the primary RPC (Alchemy, Infura…) hits its quota.
func RunPoller(ctx context.Context, primaryRPC, contractAddr string, out chan<- Price, log *zap.Logger) {
	parsedABI, err := abi.JSON(strings.NewReader(aggregatorABI))
	if err != nil {
		log.Error("failed to parse chainlink ABI — oracle disabled", zap.Error(err))
		return
	}
	addr := common.HexToAddress(contractAddr)
	callData, err := parsedABI.Pack("latestRoundData")
	if err != nil {
		log.Error("failed to pack chainlink call — oracle disabled", zap.Error(err))
		return
	}

	rpcs := buildRPCList(primaryRPC)
	idx := 0
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		rpcURL := rpcs[idx]
		client, err := ethclient.DialContext(ctx, rpcURL)
		if err != nil {
			if shouldRotate(err) {
				log.Warn("chainlink: RPC rate-limited on connect — rotating",
					zap.String("rpc", maskRPCURL(rpcURL)),
					zap.Error(err))
				idx = (idx + 1) % len(rpcs)
				backoff = time.Second
			} else {
				log.Warn("chainlink: RPC connect failed, retrying",
					zap.String("rpc", maskRPCURL(rpcURL)),
					zap.Duration("backoff", backoff),
					zap.Error(err))
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, maxBackoff)
			}
			continue
		}
		backoff = time.Second
		log.Debug("chainlink poller connected",
			zap.String("rpc", maskRPCURL(rpcURL)),
			zap.String("contract", contractAddr))

		reason := runPollerSession(ctx, client, addr, callData, parsedABI, contractAddr, out, log)
		client.Close()

		switch reason {
		case sessionEndCancelled:
			return
		case sessionEndRateLimit:
			next := (idx + 1) % len(rpcs)
			log.Warn("chainlink: RPC rate-limited — rotating to fallback",
				zap.String("from", maskRPCURL(rpcs[idx])),
				zap.String("to", maskRPCURL(rpcs[next])))
			idx = next
			backoff = time.Second
		case sessionEndDisconnect:
			log.Warn("chainlink: RPC disconnected, reconnecting",
				zap.String("rpc", maskRPCURL(rpcURL)))
		}
	}
}

// runPollerSession runs the fetch loop for one RPC connection.
// Returns the reason the session ended so RunPoller can decide whether to
// retry the same RPC (disconnect) or rotate to the next one (rate-limit).
//
// Polling interval: 5s. Chainlink BTC/USD on Polygon updates every 10–30s (or on
// >0.5% deviation), so polling every 2s was fetching the same RoundID 3–15× per
// update — pure RPC waste. At 5s we detect any new round within 5s, which is well
// within the oracle-lag strategy's minimum lag threshold of 3s (MaxOracleAge check).
//
// Deduplication: if RoundID hasn't changed since the last successful fetch, the price
// is NOT re-emitted. The event loop already holds the current price; there is no
// benefit in pushing an identical value on every tick.
func runPollerSession(ctx context.Context, client *ethclient.Client, addr common.Address, callData []byte, parsedABI abi.ABI, contractAddr string, out chan<- Price, log *zap.Logger) sessionEnd {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	consecutiveErrors := 0
	var lastRoundID uint64
	for {
		select {
		case <-ctx.Done():
			return sessionEndCancelled
		case <-ticker.C:
			p, err := fetchPrice(ctx, client, addr, callData, parsedABI)
			if err != nil {
				if shouldRotate(err) {
					log.Warn("chainlink: rate-limited", zap.Error(err))
					return sessionEndRateLimit
				}
				consecutiveErrors++
				log.Warn("chainlink fetch error", zap.Error(err), zap.Int("consecutive", consecutiveErrors))
				if consecutiveErrors >= 3 {
					return sessionEndDisconnect
				}
				continue
			}
			consecutiveErrors = 0

			// First fetch: always emit and log.
			if lastRoundID == 0 {
				lastRoundID = p.RoundID
				log.Info("chainlink first fetch OK",
					zap.String("contract", contractAddr),
					zap.Float64("price_usd", p.Value),
					zap.Time("oracle_updated_at", p.UpdatedAt),
					zap.Duration("oracle_age", time.Since(p.UpdatedAt).Truncate(time.Second)),
				)
				select {
				case out <- p:
				default:
				}
				continue
			}

			// Only emit when Chainlink has published a new round.
			if p.RoundID == lastRoundID {
				continue // same data — skip, save event-loop work
			}
			lastRoundID = p.RoundID
			log.Debug("chainlink new round",
				zap.Uint64("round_id", p.RoundID),
				zap.Float64("price_usd", p.Value),
				zap.Duration("oracle_age", time.Since(p.UpdatedAt).Truncate(time.Second)),
			)
			select {
			case out <- p:
			default:
			}
		}
	}
}

// fetchPrice calls latestRoundData on the Chainlink aggregator and returns the decoded price.
// All standard USD feeds use 8 decimal places.
func fetchPrice(ctx context.Context, client *ethclient.Client, addr common.Address, callData []byte, parsedABI abi.ABI) (Price, error) {
	msg := ethereum.CallMsg{
		To:   &addr,
		Data: callData,
	}
	result, err := client.CallContract(ctx, msg, nil)
	if err != nil {
		return Price{}, err
	}

	out, err := parsedABI.Unpack("latestRoundData", result)
	if err != nil {
		return Price{}, err
	}

	// out: [roundId uint80, answer int256, startedAt uint256, updatedAt uint256, answeredInRound uint80]
	answer, ok := out[1].(*big.Int)
	if !ok || answer == nil {
		return Price{}, fmt.Errorf("unexpected answer type from chainlink")
	}
	updatedAt, ok := out[3].(*big.Int)
	if !ok || updatedAt == nil {
		return Price{}, fmt.Errorf("unexpected updatedAt type from chainlink")
	}
	roundID, ok := out[0].(*big.Int)
	if !ok || roundID == nil {
		return Price{}, fmt.Errorf("unexpected roundId type from chainlink")
	}

	// Standard Chainlink USD feeds use 8 decimals.
	priceFloat := new(big.Float).SetInt(answer)
	priceFloat.Quo(priceFloat, new(big.Float).SetFloat64(1e8))
	price, _ := priceFloat.Float64()

	return Price{
		Value:     price,
		RoundID:   roundID.Uint64(),
		UpdatedAt: time.Unix(updatedAt.Int64(), 0),
	}, nil
}
