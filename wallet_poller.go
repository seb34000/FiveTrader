package main

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"go.uber.org/zap"
)

// usdceContract is the pUSD (Polymarket collateral token) proxy contract on Polygon Mainnet.
// balanceOf returns uint256 with 6 decimals.
const usdceContract = "0xC011a7E12a19f7B1f670d46F03B03f3342E82DFB" // pUSD proxy

// balanceOfSelector is keccak256("balanceOf(address)")[:4].
var balanceOfSelector = []byte{0x70, 0xa0, 0x82, 0x31}

// fetchOnChainUSDCBalance calls balanceOf on the USDC.e contract for the given EOA address.
func fetchOnChainUSDCBalance(ctx context.Context, client *ethclient.Client, walletAddress string) (float64, error) {
	contract := common.HexToAddress(usdceContract)
	addr := common.HexToAddress(walletAddress)

	// ABI-encode balanceOf(address): 4-byte selector + 12 bytes zero padding + 20-byte address (right-aligned in 32-byte slot, total 36 bytes).
	data := make([]byte, 36)
	copy(data[:4], balanceOfSelector)
	copy(data[16:], addr.Bytes())

	result, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &contract,
		Data: data,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("balanceOf call: %w", err)
	}
	if len(result) < 32 {
		return 0, fmt.Errorf("unexpected response length: %d bytes", len(result))
	}

	raw := new(big.Int).SetBytes(result[:32])
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(raw), big.NewFloat(1e6)).Float64()
	return f, nil
}

// runWalletBalancePoller reads the on-chain USDC.e balance every interval and stores it in bits.
// Only started in live mode; sim/dry-run use the config fallback initialised in main.
func runWalletBalancePoller(ctx context.Context, rpcURL, walletAddress string, bits *atomic.Uint64, interval time.Duration, log *zap.Logger) {
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		log.Warn("wallet balance poller: RPC dial failed — using config fallback", zap.Error(err))
		return
	}
	defer client.Close()

	fetch := func() {
		bal, err := fetchOnChainUSDCBalance(ctx, client, walletAddress)
		if err != nil {
			log.Warn("wallet balance poll failed — keeping last value", zap.Error(err))
			return
		}
		bits.Store(math.Float64bits(bal))
		log.Debug("wallet balance polled", zap.Float64("usdc", bal))
	}

	// Fetch immediately so the first trade uses the real balance, not the config fallback.
	log.Info("wallet balance poller started", zap.String("address", walletAddress), zap.String("contract", usdceContract))
	bal, err := fetchOnChainUSDCBalance(ctx, client, walletAddress)
	if err != nil {
		log.Warn("initial wallet balance fetch failed — using config fallback", zap.Error(err))
	} else {
		bits.Store(math.Float64bits(bal))
		log.Info("wallet balance initialised", zap.String("address", walletAddress), zap.String("contract", usdceContract), zap.Float64("usdc", bal))
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}
