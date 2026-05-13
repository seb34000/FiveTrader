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
	// ctfExchangeAddr = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E"
	ctfExchangeAddr = "0xE111180000d2663C0091e4f400237545B87B996B" // v2
	// NegRisk CTF Exchange on Polygon (binary UP/DOWN markets — different verifyingContract!)
	negRiskExchangeAddr = "0xe2222d279d744050d28e00520010520000310F59"
	// Zero address for open taker
	zeroAddr = "0x0000000000000000000000000000000000000000"
)

// ErrOrderKilled is returned when a FOK order is killed by the CLOB due to insufficient liquidity.
// This is a normal market outcome (not a bug) — callers should log and continue, not treat as fatal.
var ErrOrderKilled = fmt.Errorf("FOK order killed: no matching liquidity")

// ErrPriceMovedAgainstUs is returned when the live ask price has crossed the strategy's
// safety cap between signal generation and order submission. Treated like ErrOrderKilled.
var ErrPriceMovedAgainstUs = fmt.Errorf("price moved past strategy cap during signal→fill latency")

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
			{Name: "salt",          Type: "uint256"},
			{Name: "maker",         Type: "address"},
			{Name: "signer",        Type: "address"},
			{Name: "tokenId",       Type: "uint256"},
			{Name: "makerAmount",   Type: "uint256"},
			{Name: "takerAmount",   Type: "uint256"},
			{Name: "side",          Type: "uint8"},
			{Name: "signatureType", Type: "uint8"},
			{Name: "timestamp",     Type: "uint256"},
			{Name: "metadata",      Type: "bytes32"},
			{Name: "builder",       Type: "bytes32"},
		},
	}
}

// Executor signs and submits orders to Polymarket CLOB.
type Executor struct {
	key                 *ecdsa.PrivateKey
	addr                common.Address
	proxyWallet         string // if set, orders use signatureType=1 (POLY_PROXY)
	clob                *market.Client
	mode                config.Mode
	enableDumpHedgeLive bool
	slippageTicks       int // number of 0.01 ticks added to ask price to absorb staleness
	log                 *zap.Logger
}

// NewExecutor creates an Executor by loading the ECDSA private key and deriving the wallet address.
func NewExecutor(privateKeyHex string, mode config.Mode, enableDumpHedgeLive bool, slippageTicks int, log *zap.Logger) (*Executor, error) {
	keyHex := strings.TrimPrefix(privateKeyHex, "0x")
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	return &Executor{key: key, addr: addr, mode: mode, enableDumpHedgeLive: enableDumpHedgeLive, slippageTicks: slippageTicks, log: log}, nil
}

// Address returns the wallet address derived from the private key.
func (e *Executor) Address() string {
	return e.addr.Hex()
}

// ProxyWallet returns the proxy wallet address if configured, or empty string.
func (e *Executor) ProxyWallet() string {
	return e.proxyWallet
}

// SetCLOBClient updates the CLOB client after the wallet address is known.
func (e *Executor) SetCLOBClient(clob *market.Client) {
	e.clob = clob
}

// SetProxyWallet enables POLY_PROXY signing (signatureType=1).
// When set, orders are submitted with maker=proxyWallet and signer=EOA.
func (e *Executor) SetProxyWallet(addr string) {
	e.proxyWallet = addr
	e.log.Info("proxy wallet set — using signatureType=1",
		zap.String("proxy_wallet", addr),
		zap.String("signer_eoa", e.addr.Hex()),
	)
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

	// Revalidate live ask just before signing — oracle_lag bypasses MaxEntryPrice in risk
	// (cap=0.92 instead of 0.90), so a stale signal can let an order through at e.g. 0.99
	// after the orderbook caught up to the oracle. Re-check against MaxTokenPriceOracleLag.
	// Other strategies don't need this: their MaxEntryPrice cap (0.90) gives plenty of headroom.
	if sig.Strategy == strategy.NameOracleLag && e.clob != nil {
		liveAsk, err := e.clob.FetchBestAsk(ctx, sig.TokenID)
		if err != nil {
			// Non-fatal: degraded state, fall through to use the signal's stale ask.
			// We logged-and-continued historically; preserve that behaviour for resilience.
			e.log.Warn("oracle_lag revalidation failed, falling back to signal price",
				zap.String("tokenID", sig.TokenID), zap.Error(err))
		} else if liveAsk > 0 && liveAsk > strategy.MaxTokenPriceOracleLag {
			e.log.Info("oracle_lag aborted: live ask crossed cap during signal→fill latency",
				zap.Float64("signal_ask", sig.AskPrice),
				zap.Float64("live_ask", liveAsk),
				zap.Float64("cap", strategy.MaxTokenPriceOracleLag),
			)
			return "", ErrPriceMovedAgainstUs
		} else if liveAsk > 0 {
			// Use the freshest price for sizing — sig.AskPrice is replaced for the order.
			sig.AskPrice = liveAsk
		}
	}

	// Apply slippage tolerance: add N ticks to absorb orderbook staleness between
	// the last poll (~1s ago) and the moment the FOK hits the CLOB (~200ms later).
	// Capped at 0.99 to stay within valid token price range.
	bidPrice := sig.AskPrice + float64(e.slippageTicks)*0.01
	if bidPrice > 0.99 {
		bidPrice = 0.99
	}
	if e.slippageTicks > 0 {
		e.log.Debug("applying slippage",
			zap.Float64("ask", sig.AskPrice),
			zap.Float64("bid_with_slippage", bidPrice),
			zap.Int("ticks", e.slippageTicks),
		)
	}
	order, err := e.buildOrder(sig.TokenID, bidPrice, usdcSize, windowEnd, sig.NegRisk)
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
	// FOK orders are either fully matched or killed immediately.
	// "live" means the first attempt reached the server but we lost the response (Duplicated on retry).
	// In that case we can't confirm fill status — log a warning and treat as placed.
	if resp.Status == "live" && resp.OrderID == "duplicated-unknown" {
		e.log.Warn("FOK order status unknown — first attempt reached CLOB but response was lost; check positions manually")
		return resp.OrderID, nil
	}
	if resp.Status == "killed" {
		e.log.Info("FOK order not filled — no liquidity (killed by CLOB)",
			zap.String("strategy", sig.Strategy),
			zap.Float64("ask", sig.AskPrice),
			zap.Float64("usdc", usdcSize),
		)
		return "", ErrOrderKilled
	}
	if resp.Status != "matched" {
		return "", fmt.Errorf("FOK order not filled (status=%q, orderID=%s)", resp.Status, resp.OrderID)
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

	// Polymarket enforces a $1 minimum per order — skip if either leg is too small.
	// This happens when Kelly sizing produces a small total budget.
	// Required minimum: usdcSize >= sum * max(1/askUp, 1/askDown)
	const minOrderUSDC = 1.0
	if usdcUp < minOrderUSDC || usdcDown < minOrderUSDC {
		e.log.Info("dump_hedge skipped — leg size below $1 minimum",
			zap.Float64("usdc_up", usdcUp),
			zap.Float64("usdc_down", usdcDown),
			zap.Float64("total_budget", usdcSize),
		)
		return "", ErrOrderKilled
	}

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
	reqDown := market.OrderRequest{Order: orderDown, Owner: e.addr.Hex(), OrderType: "FOK", TickSize: "0.01", NegRisk: sig.NegRisk}

	// Submit both legs in parallel: reduces the window between fills from ~200ms to
	// ~network jitter (<10ms). Residual risk: if one leg fills and the other is killed,
	// naked directional exposure remains — log a critical error and require manual intervention.
	type legResult struct {
		resp *market.OrderResponse
		err  error
	}
	upCh := make(chan legResult, 1)
	downCh := make(chan legResult, 1)
	go func() { resp, err := e.retryPlaceOrder(ctx, reqUp); upCh <- legResult{resp, err} }()
	go func() { resp, err := e.retryPlaceOrder(ctx, reqDown); downCh <- legResult{resp, err} }()
	resUp := <-upCh
	resDown := <-downCh

	upOK := resUp.err == nil && resUp.resp != nil && resUp.resp.Status == "matched"
	downOK := resDown.err == nil && resDown.resp != nil && resDown.resp.Status == "matched"

	if upOK && downOK {
		e.log.Info("dump_hedge both legs placed",
			zap.String("orderIDUp", resUp.resp.OrderID),
			zap.String("orderIDDown", resDown.resp.OrderID),
		)
		return resUp.resp.OrderID + "+" + resDown.resp.OrderID, nil
	}

	if !upOK && !downOK {
		return "", ErrOrderKilled
	}

	// Partial fill — naked directional position, manual intervention required.
	if upOK {
		downStatus := "nil_response"
		if resDown.resp != nil {
			downStatus = resDown.resp.Status
		}
		e.log.Error("dump_hedge naked position: UP filled but DOWN failed — manual intervention required",
			zap.String("upOrderID", resUp.resp.OrderID),
			zap.String("downStatus", downStatus),
			zap.Error(resDown.err),
		)
	} else {
		upStatus := "nil_response"
		if resUp.resp != nil {
			upStatus = resUp.resp.Status
		}
		e.log.Error("dump_hedge naked position: DOWN filled but UP failed — manual intervention required",
			zap.String("downOrderID", resDown.resp.OrderID),
			zap.String("upStatus", upStatus),
			zap.Error(resUp.err),
		)
	}
	return "", fmt.Errorf("dump_hedge partial fill — naked position, manual intervention required")
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
		Version:           "2",
		ChainId:           ethmath.NewHexOrDecimal256(137),
		VerifyingContract: contractAddr,
	}
	// Round ask price to tick size (0.01) — CLOB rejects non-aligned prices
	askPrice = math.Floor(askPrice*100) / 100
	if askPrice <= 0 {
		return market.SignedOrder{}, fmt.Errorf("ask price rounds to zero after tick-size alignment")
	}

	// Convert USDC to micro-USDC (6 decimals), truncated to 2 decimal-place precision.
	// CLOB requires makerAmount / 1e6 to have at most 2 decimal places → divisible by 10000.
	makerRaw := int64(usdcSize * 1e6)
	makerAmount := new(big.Int).SetInt64((makerRaw / 10000) * 10000)

	// takerAmount = USDC / price * 1e6 (tokens), truncated to 4 decimal-place precision.
	// CLOB requires takerAmount / 1e6 to have at most 4 decimal places → divisible by 100.
	tokensFloat := usdcSize / askPrice
	takerRaw := int64(tokensFloat * 1e6)
	takerAmount := new(big.Int).SetInt64((takerRaw / 100) * 100)

	// Token ID as big.Int (Polymarket token IDs are large decimal strings)
	tokenIDBig := new(big.Int)
	if _, ok := tokenIDBig.SetString(tokenID, 10); !ok {
		return market.SignedOrder{}, fmt.Errorf("invalid tokenID: %s", tokenID)
	}

	// Salt: random int32 — matches SDK behaviour (TS: Date.now(), Python: randint(0, 2^31-1)).
	// Must be a JSON integer (not a string) and fit in Number.MAX_SAFE_INTEGER.
	// Using a 256-bit salt caused "Invalid order payload" because Number.parseInt loses precision.
	saltBig, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt32))
	if err != nil {
		return market.SignedOrder{}, fmt.Errorf("salt generation: %w", err)
	}
	salt := saltBig // *big.Int, small value — used in EIP-712 as uint256 (zero-padded)
	saltInt := saltBig.Int64() // int64 for JSON serialisation

	// Expiration must be 0 for FOK/GTC orders — only GTD orders use a timestamp.
	// The CLOB rejects any non-zero expiration on non-GTD orders.
	expiration := big.NewInt(0)
	_ = windowEnd // kept as parameter for future GTD support

	// Use proxy wallet (signatureType=1) when set, otherwise sign as EOA (signatureType=0).
	makerAddr := e.addr.Hex()
	sigType := int64(0)
	if e.proxyWallet != "" {
		makerAddr = e.proxyWallet
		sigType = 1
	}
	nowMs := big.NewInt(time.Now().UnixMilli())

	e.log.Debug("building order",
		zap.String("contract", contractAddr),
		zap.Bool("neg_risk", negRisk),
		zap.Int64("sig_type", sigType),
		zap.String("maker", makerAddr),
		zap.String("signer", e.addr.Hex()),
		zap.Float64("ask_price_rounded", math.Floor(askPrice*100)/100),
		zap.Float64("usdc_size", usdcSize),
	)

	order := map[string]interface{}{
		"salt":          salt,
		"maker":         makerAddr,
		"signer":        e.addr.Hex(),
		"tokenId":       tokenIDBig,
		"makerAmount":   makerAmount,
		"takerAmount":   takerAmount,
		"side":          big.NewInt(0),       // 0 = BUY
		"signatureType": big.NewInt(sigType), // 0=EOA, 1=POLY_PROXY
		"timestamp":     nowMs,
		"metadata":      common.Hash{},       // bytes32 zero — reserved field
		"builder":       common.Hash{},       // bytes32 zero — no builder code
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
		Salt:          saltInt,
		Maker:         makerAddr,
		Signer:        e.addr.Hex(),
		TokenID:       tokenID,
		MakerAmount:   makerAmount.String(),
		TakerAmount:   takerAmount.String(),
		Expiration:    expiration.String(),
		Side:          "BUY",
		SignatureType: int(sigType),
		Signature:     "0x" + hex.EncodeToString(sig),
		Timestamp:     fmt.Sprintf("%d", nowMs.Int64()),
		Builder:       common.Hash{}.Hex(),
		Metadata:      common.Hash{}.Hex(),
	}, nil
}

// retryPlaceOrder submits an order, with retry logic for GTC/GTD orders only.
// FOK orders are instant fill-or-kill: retrying the same salt causes "Duplicated".
// If a retry receives "Duplicated", it means the first attempt succeeded — we log a
// warning and return a synthetic response so the caller can continue.
func (e *Executor) retryPlaceOrder(ctx context.Context, req market.OrderRequest) (*market.OrderResponse, error) {
	// FOK orders must not be retried: same salt = "Duplicated" on second attempt.
	maxAttempts := 3
	if req.OrderType == "FOK" {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
			e.log.Warn("retrying order placement", zap.Int("attempt", attempt+1), zap.Error(lastErr))
		}
		resp, err := e.clob.PlaceOrder(ctx, req)
		if err == nil {
			return resp, nil
		}
		// "Duplicated" means the order was already accepted on a previous attempt
		// (the first attempt succeeded but we lost the response due to a network hiccup).
		// Return a synthetic response to indicate the order is live with unknown fill status.
		if strings.Contains(err.Error(), "Duplicated") {
			e.log.Warn("order already exists on CLOB (Duplicated) — first attempt succeeded but response was lost",
				zap.Int("attempt", attempt+1))
			return &market.OrderResponse{Status: "live", OrderID: "duplicated-unknown"}, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// nonLivePrefix returns the log prefix and order-ID prefix for non-live modes.
// idSuffix differentiates single-leg ("") from hedge ("hedge-") IDs.
func (e *Executor) nonLivePrefix(idSuffix string) (logPrefix, idPrefix string) {
	if e.mode == config.ModeSim {
		return "[SIM]", "sim-" + idSuffix
	}
	return "[DRY-RUN]", "dry-run-" + idSuffix
}
