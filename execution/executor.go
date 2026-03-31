package execution

import (
	"context"
	"crypto/ecdsa"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"go.uber.org/zap"

	"github.com/seb/fivetrader/config"
	"github.com/seb/fivetrader/market"
	"github.com/seb/fivetrader/strategy"
)

const (
	// CTF Exchange on Polygon (standard markets)
	ctfExchangeAddr = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E"
	// NegRisk CTF Exchange on Polygon (binary UP/DOWN markets — different verifyingContract!)
	negRiskExchangeAddr = "0xC5d563A36AE78145C45a50134d48A1215220f80a"
	// Zero address for open taker
	zeroAddr = "0x0000000000000000000000000000000000000000"
)

var (
	// orderTypes is shared between CTF and NegRisk exchanges (same struct, different domain)
	orderTypes apitypes.Types

)

// init registers the EIP-712 type definitions shared by CTF and NegRisk exchanges.
func init() {
	orderTypes = apitypes.Types{
		"EIP712Domain": {
			{Name: "name", Type: "string"},
			{Name: "version", Type: "string"},
			{Name: "chainId", Type: "uint256"},
			{Name: "verifyingContract", Type: "address"},
		},
		"Order": {
			{Name: "salt", Type: "uint256"},
			{Name: "maker", Type: "address"},
			{Name: "signer", Type: "address"},
			{Name: "taker", Type: "address"},
			{Name: "tokenId", Type: "uint256"},
			{Name: "makerAmount", Type: "uint256"},
			{Name: "takerAmount", Type: "uint256"},
			{Name: "expiration", Type: "uint256"},
			{Name: "nonce", Type: "uint256"},
			{Name: "feeRateBps", Type: "uint256"},
			{Name: "side", Type: "uint8"},
			{Name: "signatureType", Type: "uint8"},
		},
	}
}

// Executor signs and submits orders to Polymarket CLOB.
type Executor struct {
	key                 *ecdsa.PrivateKey
	addr                common.Address
	clob                *market.Client
	mode                config.Mode
	enableDumpHedgeLive bool
	log                 *zap.Logger
}

// NewExecutor creates an Executor by loading the ECDSA private key and deriving the wallet address.
func NewExecutor(privateKeyHex string, mode config.Mode, enableDumpHedgeLive bool, log *zap.Logger) (*Executor, error) {
	keyHex := strings.TrimPrefix(privateKeyHex, "0x")
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	return &Executor{key: key, addr: addr, mode: mode, enableDumpHedgeLive: enableDumpHedgeLive, log: log}, nil
}

// Address returns the wallet address derived from the private key.
func (e *Executor) Address() string {
	return e.addr.Hex()
}

// SetCLOBClient updates the CLOB client after the wallet address is known.
func (e *Executor) SetCLOBClient(clob *market.Client) {
	e.clob = clob
}

// Execute places an order for the given signal with the approved USDC size.
// Returns the order ID on success.
func (e *Executor) Execute(ctx context.Context, sig *strategy.Signal, usdcSize float64) (string, error) {
	if sig.Direction == strategy.DirectionAbstain && sig.TokenIDDown != "" {
		return e.executeDumpHedge(ctx, sig, usdcSize)
	}
	return e.executeSingleLeg(ctx, sig, usdcSize)
}

// executeSingleLeg places a single BUY order for directional signals (oracle_lag, window_delta).
func (e *Executor) executeSingleLeg(ctx context.Context, sig *strategy.Signal, usdcSize float64) (string, error) {
	if e.mode != config.ModeLive {
		logPfx, idPfx := e.nonLivePrefix("")
		tokenPrefix := sig.TokenID
		if len(tokenPrefix) > 8 {
			tokenPrefix = tokenPrefix[:8] + "..."
		}
		e.log.Info(logPfx+" would place order",
			zap.String("strategy", sig.Strategy),
			zap.String("direction", sig.Direction.String()),
			zap.String("tokenID", tokenPrefix),
			zap.Float64("price", sig.AskPrice),
			zap.Float64("usdc", usdcSize),
			zap.Float64("edge", sig.Edge),
		)
		return idPfx + fmt.Sprintf("%d", time.Now().UnixNano()), nil
	}

	windowEnd := market.WindowEnd()
	order, err := e.buildOrder(sig.TokenID, sig.AskPrice, usdcSize, windowEnd, sig.NegRisk)
	if err != nil {
		return "", err
	}

	req := market.OrderRequest{
		Order:     order,
		Owner:     e.addr.Hex(),
		OrderType: "FOK",
		TickSize:  "0.01",
		NegRisk:   sig.NegRisk,
	}
	resp, err := e.retryPlaceOrder(ctx, req)
	if err != nil {
		return "", fmt.Errorf("place order: %w", err)
	}
	e.log.Info("order placed",
		zap.String("orderID", resp.OrderID),
		zap.String("status", resp.Status),
	)
	return resp.OrderID, nil
}

// executeDumpHedge places two FOK orders (UP and DOWN) for dump_hedge arbitrage.
// Attempts to cancel the UP leg if the DOWN leg fails.
func (e *Executor) executeDumpHedge(ctx context.Context, sig *strategy.Signal, usdcSize float64) (string, error) {
	if sig.TokenID == "" || sig.TokenIDDown == "" {
		return "", fmt.Errorf("dump_hedge: missing token IDs in signal")
	}
	// dump_hedge is disabled in LIVE mode by default: two-leg FOK execution leaves a naked
	// directional position if the DOWN leg fails after UP fills (FOK cancel is a no-op).
	// Enable with ENABLE_DUMP_HEDGE_LIVE=true only when atomic two-leg submission is available.
	if e.mode == config.ModeLive && !e.enableDumpHedgeLive {
		return "", fmt.Errorf("dump_hedge disabled in LIVE mode (set ENABLE_DUMP_HEDGE_LIVE=true to override)")
	}

	// sig.AskPrice = sum (askUp + askDown); sig.AskPriceDown = askDown.
	// Equal token count on each side guarantees payout = nTokens regardless of direction.
	sum := sig.AskPrice
	askUp := sum - sig.AskPriceDown
	nTokens := usdcSize / sum
	usdcUp := nTokens * askUp
	usdcDown := nTokens * sig.AskPriceDown

	if e.mode != config.ModeLive {
		logPfx, idPfx := e.nonLivePrefix("hedge-")
		e.log.Info(logPfx+" dump_hedge would buy both sides",
			zap.Float64("ask_up", askUp),
			zap.Float64("ask_down", sig.AskPriceDown),
			zap.Float64("sum", sum),
			zap.Float64("edge", sig.Edge),
			zap.Float64("n_tokens", nTokens),
			zap.Float64("usdc_up", usdcUp),
			zap.Float64("usdc_down", usdcDown),
		)
		return idPfx + fmt.Sprintf("%d", time.Now().UnixNano()), nil
	}

	windowEnd := market.WindowEnd()
	orderUp, err := e.buildOrder(sig.TokenID, askUp, usdcUp, windowEnd, sig.NegRisk)
	if err != nil {
		return "", fmt.Errorf("dump_hedge UP leg: %w", err)
	}
	orderDown, err := e.buildOrder(sig.TokenIDDown, sig.AskPriceDown, usdcDown, windowEnd, sig.NegRisk)
	if err != nil {
		return "", fmt.Errorf("dump_hedge DOWN leg: %w", err)
	}

	reqUp := market.OrderRequest{Order: orderUp, Owner: e.addr.Hex(), OrderType: "FOK", TickSize: "0.01", NegRisk: sig.NegRisk}
	respUp, err := e.retryPlaceOrder(ctx, reqUp)
	if err != nil {
		return "", fmt.Errorf("dump_hedge UP order failed: %w", err)
	}

	reqDown := market.OrderRequest{Order: orderDown, Owner: e.addr.Hex(), OrderType: "FOK", TickSize: "0.01", NegRisk: sig.NegRisk}
	respDown, err := e.retryPlaceOrder(ctx, reqDown)
	if err != nil {
		// UP leg is placed but DOWN failed — attempt cancel to avoid naked directional exposure.
		e.log.Error("dump_hedge DOWN leg failed — cancelling UP leg",
			zap.String("upOrderID", respUp.OrderID), zap.Error(err))
		if cancelErr := e.clob.CancelOrder(ctx, respUp.OrderID); cancelErr != nil {
			e.log.Error("UP leg cancel failed — naked position, manual intervention required",
				zap.String("upOrderID", respUp.OrderID), zap.Error(cancelErr))
		}
		return "", fmt.Errorf("dump_hedge DOWN order failed (UP cancel attempted): %w", err)
	}

	e.log.Info("dump_hedge both legs placed",
		zap.String("orderIDUp", respUp.OrderID),
		zap.String("orderIDDown", respDown.OrderID),
	)
	return respUp.OrderID + "+" + respDown.OrderID, nil
}

// buildOrder constructs and EIP-712 signs a BUY order.
// negRisk must be true for binary UP/DOWN markets (uses NegRisk Exchange address).
// windowEnd is used to set the order expiration 10s before market close.
func (e *Executor) buildOrder(tokenID string, askPrice, usdcSize float64, windowEnd time.Time, negRisk bool) (market.SignedOrder, error) {
	// Select the correct verifyingContract based on market type.
	// Binary (UP/DOWN) markets use the NegRisk Exchange — wrong address = invalid signature.
	contractAddr := ctfExchangeAddr
	if negRisk {
		contractAddr = negRiskExchangeAddr
	}
	domain := apitypes.TypedDataDomain{
		Name:              "Polymarket CTF Exchange",
		Version:           "1",
		ChainId:           ethmath.NewHexOrDecimal256(137),
		VerifyingContract: contractAddr,
	}
	// Round ask price to tick size (0.01) — CLOB rejects non-aligned prices
	askPrice = math.Floor(askPrice*100) / 100
	if askPrice <= 0 {
		return market.SignedOrder{}, fmt.Errorf("ask price rounds to zero after tick-size alignment")
	}

	// Convert USDC to micro-USDC (6 decimals)
	makerAmount := new(big.Int).SetInt64(int64(usdcSize * 1e6))

	// takerAmount = USDC / price * 1e6 (tokens with 6 decimals) — truncate, never round up
	tokensFloat := usdcSize / askPrice
	takerAmount := new(big.Int).SetInt64(int64(tokensFloat * 1e6))

	// Token ID as big.Int (Polymarket token IDs are large decimal strings)
	tokenIDBig := new(big.Int)
	if _, ok := tokenIDBig.SetString(tokenID, 10); !ok {
		return market.SignedOrder{}, fmt.Errorf("invalid tokenID: %s", tokenID)
	}

	// Cryptographically secure random salt
	saltBytes := make([]byte, 32)
	if _, err := crand.Read(saltBytes); err != nil {
		return market.SignedOrder{}, fmt.Errorf("salt generation: %w", err)
	}
	salt := new(big.Int).SetBytes(saltBytes)

	// Order expires 10s before market close to avoid settlement overlap
	expiration := new(big.Int).SetInt64(windowEnd.Unix() - 10)

	order := map[string]interface{}{
		"salt":          salt,
		"maker":         e.addr.Hex(),
		"signer":        e.addr.Hex(),
		"taker":         zeroAddr,
		"tokenId":       tokenIDBig,
		"makerAmount":   makerAmount,
		"takerAmount":   takerAmount,
		"expiration":    expiration,
		"nonce":         big.NewInt(0),
		"feeRateBps":    big.NewInt(0), // must be 0
		"side":          big.NewInt(0), // 0 = BUY (uint8 for EIP-712)
		"signatureType": big.NewInt(0), // 0 = EOA
	}

	typedData := apitypes.TypedData{
		Types:       orderTypes,
		PrimaryType: "Order",
		Domain:      domain, // per-order domain with correct verifyingContract
		Message:     order,
	}

	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return market.SignedOrder{}, fmt.Errorf("EIP-712 hash: %w", err)
	}

	sig, err := crypto.Sign(hash, e.key)
	if err != nil {
		return market.SignedOrder{}, fmt.Errorf("sign: %w", err)
	}
	// Polymarket requires V = 27 or 28 (Ethereum legacy format)
	sig[64] += 27

	return market.SignedOrder{
		Salt:          salt.String(),
		Maker:         e.addr.Hex(),
		Signer:        e.addr.Hex(),
		Taker:         zeroAddr,
		TokenID:       tokenID,
		MakerAmount:   makerAmount.String(),
		TakerAmount:   takerAmount.String(),
		Expiration:    expiration.String(),
		Nonce:         "0",
		FeeRateBps:    "0",
		Side:          "BUY",
		SignatureType: "0",
		Signature:     "0x" + hex.EncodeToString(sig),
	}, nil
}

// retryPlaceOrder wraps PlaceOrder with up to 2 retries (200ms apart) on any error.
func (e *Executor) retryPlaceOrder(ctx context.Context, req market.OrderRequest) (*market.OrderResponse, error) {
	const maxAttempts = 3
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
			e.log.Warn("retrying order placement", zap.Int("attempt", attempt+1), zap.Error(err))
		}
		var resp *market.OrderResponse
		resp, err = e.clob.PlaceOrder(ctx, req)
		if err == nil {
			return resp, nil
		}
	}
	return nil, err
}

// nonLivePrefix returns the log prefix and order-ID prefix for non-live modes.
// idSuffix differentiates single-leg ("") from hedge ("hedge-") IDs.
func (e *Executor) nonLivePrefix(idSuffix string) (logPrefix, idPrefix string) {
	if e.mode == config.ModeSim {
		return "[SIM]", "sim-" + idSuffix
	}
	return "[DRY-RUN]", "dry-run-" + idSuffix
}
