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

// Price is a Chainlink oracle price update.
type Price struct {
	Value     float64
	RoundID   uint64
	UpdatedAt time.Time
}

// RunPoller polls a Chainlink aggregator at contractAddr every 2s and publishes prices.
// Retries RPC connection indefinitely with exponential backoff (1s→30s) until the context
// is cancelled. Reconnects automatically if RPC disconnects.
func RunPoller(ctx context.Context, rpcURL, contractAddr string, out chan<- Price, log *zap.Logger) {
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

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		client, err := ethclient.DialContext(ctx, rpcURL)
		if err != nil {
			log.Warn("chainlink: RPC connect failed, retrying",
				zap.String("rpc", rpcURL), zap.Duration("backoff", backoff), zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
		log.Info("chainlink poller connected", zap.String("rpc", rpcURL), zap.String("contract", contractAddr))

		disconnected := runPollerSession(ctx, client, addr, callData, parsedABI, contractAddr, out, log)
		client.Close()
		if !disconnected {
			return
		}
		log.Warn("chainlink: RPC disconnected, reconnecting", zap.String("rpc", rpcURL))
	}
}

// runPollerSession runs the fetch loop for one RPC connection.
// Returns true if the session ended due to a fetch error (reconnect needed),
// false if the context was cancelled.
func runPollerSession(ctx context.Context, client *ethclient.Client, addr common.Address, callData []byte, parsedABI abi.ABI, contractAddr string, out chan<- Price, log *zap.Logger) (reconnect bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	consecutiveErrors := 0
	var firstFetch bool
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			p, err := fetchPrice(ctx, client, addr, callData, parsedABI)
			if err != nil {
				consecutiveErrors++
				log.Warn("chainlink fetch error", zap.Error(err), zap.Int("consecutive", consecutiveErrors))
				if consecutiveErrors >= 3 {
					return true
				}
				continue
			}
			consecutiveErrors = 0
			if !firstFetch {
				firstFetch = true
				log.Info("chainlink first fetch OK",
					zap.String("contract", contractAddr),
					zap.Float64("price_usd", p.Value),
					zap.Time("oracle_updated_at", p.UpdatedAt),
					zap.Duration("oracle_age", time.Since(p.UpdatedAt).Truncate(time.Second)),
				)
			}
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
