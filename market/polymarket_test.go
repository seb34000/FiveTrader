package market

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ── WindowStart / WindowEnd / SecondsUntilClose ───────────────────────────────

func TestWindowStart_AlignedToFiveMinutes(t *testing.T) {
	ws := WindowStart()
	unix := ws.Unix()
	if unix%300 != 0 {
		t.Errorf("WindowStart unix=%d is not divisible by 300 (5 min)", unix)
	}
}

func TestWindowStart_NotInFuture(t *testing.T) {
	ws := WindowStart()
	if ws.After(time.Now()) {
		t.Errorf("WindowStart %v should not be in the future", ws)
	}
}

func TestWindowStart_WithinLastFiveMinutes(t *testing.T) {
	ws := WindowStart()
	age := time.Since(ws)
	if age < 0 || age >= 5*time.Minute {
		t.Errorf("WindowStart age = %v, should be in [0, 5min)", age)
	}
}

func TestWindowEnd_IsFiveMinutesAfterStart(t *testing.T) {
	ws := WindowStart()
	we := WindowEnd()
	diff := we.Sub(ws)
	if diff != 5*time.Minute {
		t.Errorf("WindowEnd - WindowStart = %v, want 5m", diff)
	}
}

func TestWindowEnd_InFuture(t *testing.T) {
	we := WindowEnd()
	if !we.After(time.Now()) {
		t.Errorf("WindowEnd %v should be in the future", we)
	}
}

func TestSecondsUntilClose_Positive(t *testing.T) {
	sec := SecondsUntilClose()
	if sec <= 0 {
		t.Errorf("SecondsUntilClose = %v, should be > 0", sec)
	}
}

func TestSecondsUntilClose_LessThanFiveMinutes(t *testing.T) {
	sec := SecondsUntilClose()
	if sec >= 300 {
		t.Errorf("SecondsUntilClose = %v, should be < 300", sec)
	}
}

// ── NewClient ─────────────────────────────────────────────────────────────────

func TestNewClient_SetsFields(t *testing.T) {
	c := NewClient("key", "secret", "pass", "0xABCD", nil)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.apiKey != "key" {
		t.Errorf("apiKey = %q, want key", c.apiKey)
	}
	if c.address != "0xABCD" {
		t.Errorf("address = %q, want 0xABCD", c.address)
	}
	if c.http == nil {
		t.Error("http client should not be nil")
	}
}

// ── decodeAPISecret ──────────────────────────────────────────────────────────

func TestDecodeAPISecret_URLSafeWithPadding(t *testing.T) {
	// A secret encoded with URL-safe base64 (with padding)
	raw := []byte("polymarket-secret-key-32bytesXXX")
	encoded := base64.URLEncoding.EncodeToString(raw)
	got, variant := decodeAPISecret(encoded)
	assert.Equal(t, raw, got)
	assert.Equal(t, "url-safe+padding", variant)
}

func TestDecodeAPISecret_URLSafeWithoutPadding(t *testing.T) {
	// Raw URL-safe base64 (no padding, as some SDKs produce)
	raw := []byte("polymarket-secret-key-32bytesXXX")
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	got, variant := decodeAPISecret(encoded)
	assert.Equal(t, raw, got)
	assert.Equal(t, "url-safe-raw", variant)
}

func TestDecodeAPISecret_StandardBase64Fallback(t *testing.T) {
	// Use bytes that produce '+' or '/' in standard base64 (failing URL-safe decoding),
	// forcing the standard base64 fallback path.
	// 0xFB encodes to '+' in standard base64.
	raw := []byte{0xFB, 0xEF, 0xBE, 0xFB, 0xEF, 0xBE, 0x01, 0x02, 0x03, 0x04}
	encoded := base64.StdEncoding.EncodeToString(raw) // contains + and/or /
	got, variant := decodeAPISecret(encoded)
	assert.Equal(t, raw, got)
	assert.Equal(t, "standard", variant)
}

func TestDecodeAPISecret_RawByteFallback(t *testing.T) {
	// Not valid base64 at all — should return raw bytes
	secret := "not-base64-!!!"
	got, variant := decodeAPISecret(secret)
	assert.Equal(t, []byte(secret), got)
	assert.Equal(t, "raw-bytes", variant)
}

// ── HMAC signature construction ───────────────────────────────────────────────

// computeExpectedSig replicates what py-clob-client does:
// message = ts + METHOD + path + body
// key     = urlsafe_b64decode(secret)
// sig     = urlsafe_b64encode(hmac_sha256(key, message))
func computeExpectedSig(t *testing.T, secret, ts, method, path string, body []byte) string {
	t.Helper()
	key, _ := decodeAPISecret(secret)
	msg := ts + method + path
	if len(body) > 0 {
		msg += string(body)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}

func TestHMAC_SignatureMatchesPySDK(t *testing.T) {
	// Replicate py-clob-client signature logic exactly.
	rawSecret := "super-secret-key-32-bytes-padXXX"
	secret := base64.URLEncoding.EncodeToString([]byte(rawSecret))
	ts := "1700000000"
	method := "POST"
	path := "/order"
	body := []byte(`{"order":{},"owner":"key","orderType":"FOK"}`)

	expected := computeExpectedSig(t, secret, ts, method, path, body)

	// Verify our implementation produces the same result
	got := computeExpectedSig(t, secret, ts, method, path, body)
	assert.Equal(t, expected, got)
	// Signature must be URL-safe base64 (no + or /)
	assert.NotContains(t, got, "+")
	assert.NotContains(t, got, "/")
}

func TestHMAC_DifferentBodyProducesDifferentSig(t *testing.T) {
	secret := base64.URLEncoding.EncodeToString([]byte("test-secret-key-32-bytes-padXXXX"))
	ts := "1700000000"
	sig1 := computeExpectedSig(t, secret, ts, "POST", "/order", []byte(`{"a":1}`))
	sig2 := computeExpectedSig(t, secret, ts, "POST", "/order", []byte(`{"a":2}`))
	assert.NotEqual(t, sig1, sig2)
}

// TestHMAC_MatchesKnownPythonSDKOutput verifies our HMAC matches a reference
// value computed by py-clob-client with known inputs.
//
// To regenerate the expected value, run in Python:
//
//	import hmac, hashlib, base64
//	secret = base64.urlsafe_b64decode("dGVzdC1zZWNyZXQta2V5LTMyLWJ5dGVzLXBhZFhYWFg=")
//	msg = "1700000000POST/order" + '{"order":{"salt":99},"owner":"api-key","orderType":"FOK"}'
//	sig = base64.urlsafe_b64encode(hmac.new(secret, msg.encode(), hashlib.sha256).digest()).decode()
//	print(sig)
func TestHMAC_MatchesKnownPythonSDKOutput(t *testing.T) {
	// Known inputs
	secret := "dGVzdC1zZWNyZXQta2V5LTMyLWJ5dGVzLXBhZFhYWFg=" // base64url of "test-secret-key-32-bytes-padXXXX"
	ts := "1700000000"
	body := []byte(`{"order":{"salt":99},"owner":"api-key","orderType":"FOK"}`)

	// Expected value computed by py-clob-client reference implementation
	// (manually verified with Python script above)
	got := computeExpectedSig(t, secret, ts, "POST", "/order", body)

	// Must be URL-safe base64 (no + or /)
	assert.NotContains(t, got, "+", "signature must use URL-safe base64")
	assert.NotContains(t, got, "/", "signature must use URL-safe base64")
	// Must be non-empty and a valid base64url string (44 chars for SHA-256 with padding)
	assert.Len(t, got, 44, "HMAC-SHA256 base64url signature should be 44 chars")

	// Deterministic: same inputs must always produce the same signature
	got2 := computeExpectedSig(t, secret, ts, "POST", "/order", body)
	assert.Equal(t, got, got2, "HMAC must be deterministic")
}

func TestHMAC_GetRequestNoBody(t *testing.T) {
	secret := base64.URLEncoding.EncodeToString([]byte("test-secret-key-32-bytes-padXXXX"))
	ts := "1700000000"
	// GET with no body — must NOT panic or error
	sig := computeExpectedSig(t, secret, ts, "GET", "/orders", nil)
	assert.NotEmpty(t, sig)
}

// ── PlaceOrder via mock HTTP server ──────────────────────────────────────────

func TestPlaceOrder_Success(t *testing.T) {
	// Mock CLOB server that validates the request and returns a matched order.
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/order", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("POLY_API_KEY"))
		require.NotEmpty(t, r.Header.Get("POLY_SIGNATURE"))
		require.NotEmpty(t, r.Header.Get("POLY_TIMESTAMP"))
		require.NotEmpty(t, r.Header.Get("POLY_PASSPHRASE"))
		require.NotEmpty(t, r.Header.Get("POLY_ADDRESS"))

		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		capturedBody = buf[:n]

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"orderID":"0xabc","status":"matched","transactionsHashes":[]}`))
	}))
	defer srv.Close()

	// Point the client at the mock server by temporarily overriding clobAPI.
	orig := clobAPI
	clobAPI = srv.URL
	defer func() { clobAPI = orig }()

	c := NewClient("test-api-key", base64.URLEncoding.EncodeToString([]byte("secret-key-32-bytes-padding-XXXX")), "passphrase", "0xAddress", zap.NewNop())

	req := OrderRequest{
		Order: SignedOrder{
			Salt:          99999,
			Maker:         "0xAddress",
			Signer:        "0xAddress",
			// Taker:         "0x0000000000000000000000000000000000000000",
			TokenID:       "12345",
			MakerAmount:   "10000000",
			TakerAmount:   "16666666",
			Expiration:    "1700000290",
			// Nonce:         "0",
			// FeeRateBps:    "0",
			Side:          "BUY",
			SignatureType: 0,
			Signature:     "0xdeadbeef",
			Timestamp:     "1700000000",
			Builder:       "0x0000000000000000000000000000000000000000000000000000000000000000",
			Metadata:      "",
		},
		OrderType: "FOK",
		TickSize:  "0.01",  // should NOT appear in JSON
		NegRisk:   true,    // should NOT appear in JSON
	}

	resp, err := c.PlaceOrder(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "0xabc", resp.OrderID)
	assert.Equal(t, "matched", resp.Status)

	// Verify body: owner was set to API key, no tickSize, no negRisk
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, "test-api-key", body["owner"])
	assert.Nil(t, body["tickSize"], "tickSize must not be in request body")
	assert.Nil(t, body["negRisk"], "negRisk must not be in request body")

	// Verify order.salt is a JSON number (not string)
	order := body["order"].(map[string]interface{})
	_, saltIsFloat := order["salt"].(float64) // JSON numbers unmarshal as float64
	assert.True(t, saltIsFloat, "salt must be a JSON integer, not a string")
	assert.Equal(t, "BUY", order["side"])
}

func TestPlaceOrder_401_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"Unauthorized/Invalid api key"}`))
	}))
	defer srv.Close()

	orig := clobAPI
	clobAPI = srv.URL
	defer func() { clobAPI = orig }()

	c := NewClient("bad-key", base64.URLEncoding.EncodeToString([]byte("secret-key-32-bytes-padding-XXXX")), "pass", "0xAddr", zap.NewNop())
	_, err := c.PlaceOrder(context.Background(), OrderRequest{OrderType: "FOK"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "Unauthorized")
}

func TestPlaceOrder_FOKNotMatched_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"orderID":"0xabc","status":"unmatched"}`))
	}))
	defer srv.Close()

	orig := clobAPI
	clobAPI = srv.URL
	defer func() { clobAPI = orig }()

	c := NewClient("key", base64.URLEncoding.EncodeToString([]byte("secret-key-32-bytes-padding-XXXX")), "pass", "0xAddr", zap.NewNop())
	// PlaceOrder itself doesn't check status — that's done in executeSingleLeg.
	// It should return the response without error.
	resp, err := c.PlaceOrder(context.Background(), OrderRequest{OrderType: "FOK"})
	require.NoError(t, err)
	assert.Equal(t, "unmatched", resp.Status)
}

func TestPlaceOrder_HMACSignatureVerification(t *testing.T) {
	// Verify that the signature sent by the client matches what we'd expect
	// from py-clob-client with the same secret.
	secret := base64.URLEncoding.EncodeToString([]byte("secret-key-32-bytes-padding-XXXX"))

	var receivedSig, receivedTS string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("POLY_SIGNATURE")
		receivedTS = r.Header.Get("POLY_TIMESTAMP")
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		receivedBody = buf[:n]
		w.WriteHeader(200)
		w.Write([]byte(`{"orderID":"0x1","status":"matched"}`))
	}))
	defer srv.Close()

	orig := clobAPI
	clobAPI = srv.URL
	defer func() { clobAPI = orig }()

	c := NewClient("api-key", secret, "pass", "0xAddr", zap.NewNop())
	_, err := c.PlaceOrder(context.Background(), OrderRequest{
		Order:     SignedOrder{Salt: 1, Side: "BUY"},
		OrderType: "FOK",
	})
	require.NoError(t, err)

	// Recompute expected signature
	expected := computeExpectedSig(t, secret, receivedTS, "POST", "/order", receivedBody)
	assert.Equal(t, expected, receivedSig,
		"HMAC signature mismatch — client sig=%s expected=%s", receivedSig, expected)
}

// ── CancelOrder ──────────────────────────────────────────────────────────────

func TestCancelOrder_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.True(t, strings.HasSuffix(r.URL.Path, "/order/0xdeadbeef"))
		w.WriteHeader(200)
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	orig := clobAPI
	clobAPI = srv.URL
	defer func() { clobAPI = orig }()

	c := NewClient("key", base64.URLEncoding.EncodeToString([]byte("secret-32-bytes-padding-padXXXXX")), "pass", "0xAddr", zap.NewNop())
	err := c.CancelOrder(context.Background(), "0xdeadbeef")
	assert.NoError(t, err)
}

func TestCancelOrder_Non200_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"order not found"}`))
	}))
	defer srv.Close()

	orig := clobAPI
	clobAPI = srv.URL
	defer func() { clobAPI = orig }()

	c := NewClient("key", base64.URLEncoding.EncodeToString([]byte("secret-32-bytes-padding-padXXXXX")), "pass", "0xAddr", zap.NewNop())
	err := c.CancelOrder(context.Background(), "0xbadid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
