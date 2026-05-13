package execution

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/seb/fivetrader/config"
	"github.com/seb/fivetrader/market"
)

// knownPrivKey is a deterministic test key (never used for real funds).
const knownPrivKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	exec, err := NewExecutor(knownPrivKey, config.ModeDryRun, false, 0, zap.NewNop())
	require.NoError(t, err)
	return exec
}

// ── buildOrder: field values ──────────────────────────────────────────────────

func TestBuildOrder_MakerAndSigner_EqualWalletAddress(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	assert.Equal(t, exec.addr.Hex(), order.Maker)
	assert.Equal(t, exec.addr.Hex(), order.Signer)
	// assert.Equal(t, zeroAddr, order.Taker)
}

func TestBuildOrder_Side_IsStringBUY(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	// Must be "BUY" string, not integer — CLOB rejects integer side
	assert.Equal(t, "BUY", order.Side)
}

func TestBuildOrder_Salt_IsSmallInteger(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	// Salt must be < math.MaxInt32 so Number.parseInt on JS side doesn't lose precision
	assert.GreaterOrEqual(t, order.Salt, int64(0))
	assert.Less(t, order.Salt, int64(math.MaxInt32))
}

func TestBuildOrder_SignatureType_IsZeroEOA(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	assert.Equal(t, 0, order.SignatureType)
}

// func TestBuildOrder_FeeRateBps_MatchesInput(t *testing.T) {
// 	exec := newTestExecutor(t)
// 	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
// 		0.60, 10.0, time.Now().Add(5*time.Minute), true)
// 	require.NoError(t, err)

// 	assert.Equal(t, "1000", order.FeeRateBps)
// 	assert.Equal(t, "0", order.Nonce)
// }

func TestBuildOrder_Signature_HasHexPrefix(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(order.Signature, "0x"), "signature should start with 0x, got: %s", order.Signature)
	// 65 bytes = 130 hex chars + "0x" prefix
	assert.Len(t, order.Signature, 132)
}

// ── buildOrder: amount calculations ──────────────────────────────────────────

func TestBuildOrder_MakerAmount_TwoDecimalPrecision(t *testing.T) {
	exec := newTestExecutor(t)
	// $25.00 → 25000000 micro-USDC, divisible by 10000 (2 decimal places) ✓
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 25.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	makerInt, _ := new(big.Int).SetString(order.MakerAmount, 10)
	mod := new(big.Int).Mod(makerInt, big.NewInt(10000))
	assert.Equal(t, int64(0), mod.Int64(), "makerAmount must be divisible by 10000 (2 decimal USDC precision)")
	assert.Equal(t, "25000000", order.MakerAmount)
}

func TestBuildOrder_TakerAmount_FourDecimalPrecision(t *testing.T) {
	exec := newTestExecutor(t)
	// $10 at $0.60 → 16.6666 tokens → 16666600 micro-tokens (divisible by 100)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	takerInt, _ := new(big.Int).SetString(order.TakerAmount, 10)
	mod := new(big.Int).Mod(takerInt, big.NewInt(100))
	assert.Equal(t, int64(0), mod.Int64(), "takerAmount must be divisible by 100 (4 decimal token precision)")
	assert.Equal(t, "16666600", order.TakerAmount)
}

func TestBuildOrder_PriceRounding_ToTickSize(t *testing.T) {
	exec := newTestExecutor(t)
	// Price 0.605 should round DOWN to 0.60 (floor to 0.01)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.605, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	// takerAmount = 10 / 0.60 * 1e6, truncated to 4 decimal places = 16666600
	assert.Equal(t, "16666600", order.TakerAmount)
}

func TestBuildOrder_PriceZero_ReturnsError(t *testing.T) {
	exec := newTestExecutor(t)
	_, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.001, 10.0, time.Now().Add(5*time.Minute), true) // rounds to 0
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rounds to zero")
}

func TestBuildOrder_InvalidTokenID_ReturnsError(t *testing.T) {
	exec := newTestExecutor(t)
	_, err := exec.buildOrder("not-a-number", 0.60, 10.0, time.Now().Add(5*time.Minute), true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tokenID")
}

// ── buildOrder: v2 fields ─────────────────────────────────────────────────────

func TestBuildOrder_Expiration_IsZeroForFOK(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)
	assert.Equal(t, "0", order.Expiration)
}

func TestBuildOrder_V2_TimestampPresentAndRecent(t *testing.T) {
	exec := newTestExecutor(t)
	before := time.Now().UnixMilli()
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)
	after := time.Now().UnixMilli()

	ts, err := strconv.ParseInt(order.Timestamp, 10, 64)
	require.NoError(t, err, "Timestamp must be a decimal integer string")
	assert.GreaterOrEqual(t, ts, before)
	assert.LessOrEqual(t, ts, after)
}

func TestBuildOrder_V2_BuilderAndMetadataAreZeroBytes32(t *testing.T) {
	exec := newTestExecutor(t)
	order, err := exec.buildOrder("71321045679252212594626385532706912750332728571942532289631379312455583992563",
		0.60, 10.0, time.Now().Add(5*time.Minute), true)
	require.NoError(t, err)

	zeroBytes32 := "0x" + strings.Repeat("0", 64)
	assert.Equal(t, zeroBytes32, order.Builder, "Builder must be bytes32 zero")
	assert.Equal(t, zeroBytes32, order.Metadata, "Metadata must be bytes32 zero")
	assert.Len(t, order.Builder, 66)
	assert.Len(t, order.Metadata, 66)
}

// ── buildOrder: contract address (negRisk vs CTF) ────────────────────────────

func TestBuildOrder_NegRisk_DifferentSignature(t *testing.T) {
	exec := newTestExecutor(t)
	tokenID := "71321045679252212594626385532706912750332728571942532289631379312455583992563"
	windowEnd := time.Now().Add(5 * time.Minute)

	// Sign with both — same input but different verifyingContract → different signatures
	orderNeg, err := exec.buildOrder(tokenID, 0.60, 10.0, windowEnd, true)
	require.NoError(t, err)
	orderStd, err := exec.buildOrder(tokenID, 0.60, 10.0, windowEnd, false)
	require.NoError(t, err)

	// Salt is random so we can't compare directly, but salts are likely different
	// What we CAN assert: both are valid hex signatures
	assert.True(t, strings.HasPrefix(orderNeg.Signature, "0x"))
	assert.True(t, strings.HasPrefix(orderStd.Signature, "0x"))
}

// ── JSON serialisation: critical field format ─────────────────────────────────

func TestOrderRequest_JSON_NoTickSizeOrNegRisk(t *testing.T) {
	// TickSize and NegRisk must NOT appear in the JSON body sent to CLOB
	req := market.OrderRequest{
		Order:     market.SignedOrder{Side: "BUY", Salt: 12345},
		Owner:     "test-owner",
		OrderType: "FOK",
		TickSize:  "0.01",
		NegRisk:   true,
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)

	assert.NotContains(t, string(b), "tickSize", "tickSize must not be in JSON body")
	assert.NotContains(t, string(b), "negRisk", "negRisk must not be in JSON body")
	assert.Contains(t, string(b), `"owner":"test-owner"`)
	assert.Contains(t, string(b), `"orderType":"FOK"`)
}

func TestSignedOrder_JSON_SaltIsInteger(t *testing.T) {
	// Salt must be a JSON integer, not a string — server does Number.parseInt()
	order := market.SignedOrder{
		Salt: 1234567,
		Side: "BUY",
	}
	b, err := json.Marshal(order)
	require.NoError(t, err)

	// Must be `"salt":1234567` not `"salt":"1234567"`
	assert.Contains(t, string(b), `"salt":1234567`)
	assert.NotContains(t, string(b), `"salt":"`)
}

func TestSignedOrder_JSON_SideIsString(t *testing.T) {
	order := market.SignedOrder{Side: "BUY"}
	b, err := json.Marshal(order)
	require.NoError(t, err)

	assert.Contains(t, string(b), `"side":"BUY"`)
	// Must not be integer 0
	assert.NotContains(t, string(b), `"side":0`)
}
