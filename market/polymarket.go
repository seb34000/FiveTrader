package market

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const gammaAPI = "https://gamma-api.polymarket.com"

// clobAPI is a var so tests can point it at a mock server.
var clobAPI = "https://clob.polymarket.com"

// State is the current snapshot of a crypto 5min market window.
type State struct {
	ConditionID string
	TokenIDUp   string
	TokenIDDown string
	AskUp       float64
	BidUp       float64
	AskDown     float64
	BidDown     float64
	WindowStart time.Time
	WindowEnd   time.Time
	NegRisk     bool  // true if market uses NegRisk CTF exchange
	FeeRateBps  int64 // taker fee in basis points (0 = free, 1000 = 10%)
}

// Client interacts with Polymarket Gamma + CLOB APIs.
type Client struct {
	http       *http.Client
	apiKey     string
	apiSecret  string
	passphrase string
	address    string
	log        *zap.Logger
}

// NewClient creates a Polymarket HTTP client authenticated with the given CLOB API credentials.
// The transport keeps connections alive so that order submission reuses existing TCP+TLS
// sessions instead of paying the handshake cost (~50-150ms) on every request.
func NewClient(apiKey, apiSecret, passphrase, address string, log *zap.Logger) *Client {
	transport := &http.Transport{
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		DisableKeepAlives:     false,
	}
	return &Client{
		http:       &http.Client{Timeout: 10 * time.Second, Transport: transport},
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		address:    address,
		log:        log,
	}
}

// WindowStart returns the start of the current 5-minute window.
func WindowStart() time.Time {
	now := time.Now().Unix()
	return time.Unix(now-(now%300), 0)
}

// WindowEnd returns the end of the current 5-minute window.
func WindowEnd() time.Time {
	return WindowStart().Add(5 * time.Minute)
}

// SecondsUntilClose returns seconds remaining in the current window.
func SecondsUntilClose() float64 {
	return time.Until(WindowEnd()).Seconds()
}

// RunWatcher polls the market for slugPrefix (e.g. "btc-updown-5m") every 200ms
// and publishes state updates. 200ms keeps orderbook staleness under ~400ms total
// (poll latency + event loop processing), vs ~1.2s at 1s interval.
func RunWatcher(ctx context.Context, c *Client, slugPrefix string, out chan<- State, log *zap.Logger) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := c.FetchState(ctx, slugPrefix)
			if err != nil {
				log.Warn("market fetch error", zap.String("slug_prefix", slugPrefix), zap.Error(err))
				continue
			}
			select {
			case out <- state:
			default:
			}
		}
	}
}

// FetchState looks up the current 5min market for slugPrefix and its order book.
func (c *Client) FetchState(ctx context.Context, slugPrefix string) (State, error) {
	ws := WindowStart()
	slug := fmt.Sprintf("%s-%d", slugPrefix, ws.Unix())

	info, err := c.findMarket(ctx, slug)
	if err != nil {
		return State{}, fmt.Errorf("market discovery: %w", err)
	}

	// Fetch both orderbooks in parallel to minimise staleness between UP and DOWN prices.
	var (
		bookUp, bookDown book
		errUp, errDown   error
		wg               sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); bookUp, errUp = c.fetchOrderBook(ctx, info.TokenIDUp) }()
	go func() { defer wg.Done(); bookDown, errDown = c.fetchOrderBook(ctx, info.TokenIDDown) }()
	wg.Wait()
	if errUp != nil {
		return State{}, fmt.Errorf("book UP: %w", errUp)
	}
	if errDown != nil {
		return State{}, fmt.Errorf("book DOWN: %w", errDown)
	}

	return State{
		ConditionID: info.ConditionID,
		TokenIDUp:   info.TokenIDUp,
		TokenIDDown: info.TokenIDDown,
		AskUp:       bookUp.BestAsk,
		BidUp:       bookUp.BestBid,
		AskDown:     bookDown.BestAsk,
		BidDown:     bookDown.BestBid,
		WindowStart: ws,
		WindowEnd:   ws.Add(5 * time.Minute),
		NegRisk:     info.NegRisk,
		FeeRateBps:  info.FeeRateBps,
	}, nil
}

type marketInfo struct {
	ConditionID string
	TokenIDUp   string
	TokenIDDown string
	NegRisk     bool
	FeeRateBps  int64
}

// gammaMarket is the Gamma API market object returned in the array response.
// clobTokenIds and outcomes are JSON-encoded string arrays (encoded inside a string).
type gammaMarket struct {
	ConditionID   string `json:"conditionId"`
	ClobTokenIds  string `json:"clobTokenIds"`
	Outcomes      string `json:"outcomes"`
	NegRisk       bool   `json:"negRisk"`
	MakerBaseFee  int64  `json:"makerBaseFee"`
	TakerBaseFee  int64  `json:"takerBaseFee"`
}

// gammaMarketResolved extends gammaMarket with resolution fields.
type gammaMarketResolved struct {
	ConditionID   string `json:"conditionId"`
	Resolved      bool   `json:"resolved"`
	OutcomePrices string `json:"outcomePrices"` // JSON-encoded string array, e.g. "[\"1\",\"0\"]"
	Outcomes      string `json:"outcomes"`       // JSON-encoded string array, e.g. "[\"UP\",\"DOWN\"]"
}

// Resolution holds the resolved outcome of a settled market.
type Resolution struct {
	Resolved bool
	Winner   string // "UP" or "DOWN"; empty if not yet resolved
}

// GetResolution queries the Gamma API to determine whether a market has settled.
// Returns Resolution{Resolved: false} if the market window is still open.
func (c *Client) GetResolution(ctx context.Context, conditionID string) (Resolution, error) {
	url := fmt.Sprintf("%s/markets?conditionId=%s", gammaAPI, conditionID)
	body, err := c.get(ctx, url)
	if err != nil {
		return Resolution{}, err
	}
	var markets []gammaMarketResolved
	if err := json.Unmarshal(body, &markets); err != nil {
		return Resolution{}, fmt.Errorf("parse resolution response: %w", err)
	}
	if len(markets) == 0 {
		return Resolution{}, fmt.Errorf("no market found for conditionID %s", conditionID)
	}
	m := markets[0]
	if !m.Resolved {
		return Resolution{}, nil
	}
	var prices, outcomes []string
	if err := json.Unmarshal([]byte(m.OutcomePrices), &prices); err != nil {
		return Resolution{}, fmt.Errorf("parse outcomePrices: %w", err)
	}
	if err := json.Unmarshal([]byte(m.Outcomes), &outcomes); err != nil {
		return Resolution{}, fmt.Errorf("parse outcomes: %w", err)
	}
	for i, p := range prices {
		if (p == "1" || p == "1.0") && i < len(outcomes) {
			return Resolution{Resolved: true, Winner: strings.ToUpper(outcomes[i])}, nil
		}
	}
	return Resolution{}, fmt.Errorf("cannot determine winner from outcomePrices=%v outcomes=%v", prices, outcomes)
}

// findMarket queries the Gamma API for the market matching slug and returns its token IDs.
func (c *Client) findMarket(ctx context.Context, slug string) (marketInfo, error) {
	url := fmt.Sprintf("%s/markets?slug=%s", gammaAPI, slug)
	body, err := c.get(ctx, url)
	if err != nil {
		return marketInfo{}, err
	}

	var markets []gammaMarket
	if err := json.Unmarshal(body, &markets); err != nil {
		return marketInfo{}, fmt.Errorf("parse gamma response: %w", err)
	}
	if len(markets) == 0 {
		return marketInfo{}, fmt.Errorf("no market found for slug %s", slug)
	}
	m := markets[0]

	// clobTokenIds and outcomes are JSON strings containing arrays, e.g. "[\"id1\",\"id2\"]"
	var tokenIDs []string
	if err := json.Unmarshal([]byte(m.ClobTokenIds), &tokenIDs); err != nil {
		return marketInfo{}, fmt.Errorf("parse clobTokenIds: %w", err)
	}
	var outcomes []string
	if err := json.Unmarshal([]byte(m.Outcomes), &outcomes); err != nil {
		return marketInfo{}, fmt.Errorf("parse outcomes: %w", err)
	}
	if len(tokenIDs) < 2 || len(outcomes) < 2 {
		return marketInfo{}, fmt.Errorf("insufficient tokens/outcomes in market response")
	}

	// FeeRateBps in the signed EIP-712 order must be 0: the CLOB applies market-level
	// fees separately after matching. Using m.TakerBaseFee here causes order_version_mismatch.
	info := marketInfo{ConditionID: m.ConditionID, NegRisk: m.NegRisk, FeeRateBps: 0}
	for i, outcome := range outcomes {
		if i >= len(tokenIDs) {
			break
		}
		switch strings.ToUpper(outcome) {
		case "UP":
			info.TokenIDUp = tokenIDs[i]
		case "DOWN":
			info.TokenIDDown = tokenIDs[i]
		}
	}
	if info.TokenIDUp == "" || info.TokenIDDown == "" {
		return marketInfo{}, fmt.Errorf("could not find UP/DOWN tokens (conditionID=%s)", m.ConditionID)
	}
	return info, nil
}

type orderBookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type orderBookResponse struct {
	Bids []orderBookLevel `json:"bids"`
	Asks []orderBookLevel `json:"asks"`
}

type book struct {
	BestAsk float64
	BestBid float64
}

// fetchOrderBook retrieves the best ask and bid for a given CLOB token ID.
func (c *Client) fetchOrderBook(ctx context.Context, tokenID string) (book, error) {
	url := fmt.Sprintf("%s/book?token_id=%s", clobAPI, tokenID)
	body, err := c.get(ctx, url)
	if err != nil {
		return book{}, err
	}

	var resp orderBookResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return book{}, err
	}

	b := book{}
	// Parse and find best ask (lowest) and best bid (highest) without sorting.
	for _, level := range resp.Asks {
		p, err := strconv.ParseFloat(level.Price, 64)
		if err != nil || p <= 0 {
			continue
		}
		if b.BestAsk <= 0 || p < b.BestAsk {
			b.BestAsk = p
		}
	}
	for _, level := range resp.Bids {
		p, err := strconv.ParseFloat(level.Price, 64)
		if err != nil || p <= 0 {
			continue
		}
		if p > b.BestBid {
			b.BestBid = p
		}
	}
	return b, nil
}

// FetchBestAsk returns the current best ask price for a single CLOB token.
// Used by the executor to revalidate stale prices just before placing an order
// (signal→fill latency on the HTTPS path is ~85–260ms; high-vol periods can move
// the book several ticks during that window).
func (c *Client) FetchBestAsk(ctx context.Context, tokenID string) (float64, error) {
	b, err := c.fetchOrderBook(ctx, tokenID)
	if err != nil {
		return 0, err
	}
	return b.BestAsk, nil
}

// CancelOrder cancels an open order by ID via DELETE /order/{orderID}.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	resp, err := c.doWithAuth(ctx, "DELETE", "/order/"+orderID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel order %s: HTTP %d: %s", orderID, resp.StatusCode, string(body))
	}
	return nil
}

// PlaceOrder submits a signed order to the CLOB API.
// The signature and order fields are pre-computed by the executor.
// owner must be the CLOB API key (not the wallet address) — that is what
// the CLOB authenticates against when validating the payload.
func (c *Client) PlaceOrder(ctx context.Context, req OrderRequest) (*OrderResponse, error) {
	req.Owner = c.apiKey // always override: CLOB expects API key, not wallet address
	body, err := json.Marshal(req)
	if err == nil {
		c.log.Debug("placing order", zap.ByteString("payload", body))
	}
	if err != nil {
		return nil, err
	}

	httpResp, err := c.doWithAuth(ctx, "POST", "/order", body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode != 200 {
		// FOK orders that can't be fully filled return HTTP 400 with a specific message.
		// This is a normal "no liquidity" outcome, not an error — return status="killed".
		if httpResp.StatusCode == 400 && strings.Contains(string(respBody), "FOK orders are fully filled or killed") {
			return &OrderResponse{Status: "killed"}, nil
		}
		return nil, fmt.Errorf("CLOB API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var orderResp OrderResponse
	if err := json.Unmarshal(respBody, &orderResp); err != nil {
		return nil, err
	}
	return &orderResp, nil
}

// OrderRequest is the JSON body for POST /order.
// tickSize and negRisk are NOT sent — confirmed absent from both TS and Python SDK sources.
type OrderRequest struct {
	Order     SignedOrder `json:"order"`
	Owner     string     `json:"owner"`
	OrderType string     `json:"orderType"` // "FOK", "GTC", "GTD"
	TickSize  string     `json:"-"`         // local only — not sent to API
	NegRisk   bool       `json:"-"`         // local only — not sent to API
}

// SignedOrder is the EIP-712 signed order struct sent to the CLOB.
// Field types match the official TypeScript SDK orderToJson() in clob-client/src/utilities.ts:
//   Salt          → JSON integer (Number.parseInt), NOT a string
//   Side          → "BUY"/"SELL" string enum
//   SignatureType → integer (0=EOA)
//   Amounts       → decimal strings
type SignedOrder struct {
	Salt          int64  `json:"salt"`          // JSON integer — server does Number.parseInt
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	// Taker         string `json:"taker"`
	TokenID       string `json:"tokenId"`
	MakerAmount   string `json:"makerAmount"`
	TakerAmount   string `json:"takerAmount"`
	Expiration    string `json:"expiration"`
	// Nonce         string `json:"nonce"`
	// FeeRateBps    string `json:"feeRateBps"`
	Side          string `json:"side"`          // "BUY" or "SELL" (string enum, matches TS SDK)
	SignatureType int    `json:"signatureType"` // 0=EOA, 1=POLY_PROXY, 2=POLY_GNOSIS_SAFE
	Signature     string `json:"signature"`
	Timestamp     string `json:"timestamp"`		// since v2, 1735689600000
	Builder	  string `json:"builder"`		// since v2, e.g. 0x0000000000000000000000000000000000000000000000000000000000000000
	Metadata	  string `json:"metadata"`		// since v2, optional freeform string field
}

// OrderResponse from CLOB POST /order.
type OrderResponse struct {
	OrderID         string   `json:"orderID"`
	Status          string   `json:"status"`
	TransactionHashes []string `json:"transactionsHashes,omitempty"`
}

// doWithAuth executes an HMAC-signed HTTP request against the CLOB API.
func (c *Client) doWithAuth(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	// Polymarket CLOB signs over path WITHOUT query string (mirrors py-clob-client behaviour).
	// Query params are included in the HTTP request URL but excluded from the HMAC message.
	hmacPath := path
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		hmacPath = path[:idx]
	}
	msgParts := ts + method + hmacPath
	if len(body) > 0 {
		msgParts += string(body)
	}
	// The API secret is URL-safe base64-encoded (py-clob-client uses urlsafe_b64decode).
	// Try URL-safe with and without padding, then standard base64, then raw bytes.
	secretBytes, secretVariant := decodeAPISecret(c.apiSecret)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(msgParts))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	bodyPreview := ""
	if len(body) > 0 && len(body) <= 120 {
		bodyPreview = string(body)
	} else if len(body) > 120 {
		bodyPreview = string(body[:120]) + "..."
	}
	c.log.Debug("auth request",
		zap.String("method", method),
		zap.String("path", path),
		zap.String("ts", ts),
		zap.String("address", c.address),
		zap.String("api_key", func() string {
			if len(c.apiKey) > 8 {
				return c.apiKey[:8] + "..."
			}
			return c.apiKey
		}()),
		zap.String("secret_variant", secretVariant),
		zap.Int("secret_key_bytes", len(secretBytes)),
		zap.String("sig_prefix", sig[:16]+"..."),
		zap.String("hmac_msg_preview", ts+method+path+bodyPreview),
	)

	req, err := http.NewRequestWithContext(ctx, method, clobAPI+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("POLY_ADDRESS", c.address)
	req.Header.Set("POLY_SIGNATURE", sig)
	req.Header.Set("POLY_TIMESTAMP", ts)
	req.Header.Set("POLY_API_KEY", c.apiKey)
	req.Header.Set("POLY_PASSPHRASE", c.passphrase)

	return c.http.Do(req)
}

// ValidateAuth verifies HMAC credentials are correct by calling
// GET /balance-allowance (an authenticated endpoint that always exists).
// Call this on startup in live mode to catch misconfigured API keys early.
func (c *Client) ValidateAuth(ctx context.Context) error {
	resp, err := c.doWithAuth(ctx, "GET", "/balance-allowance?asset_type=COLLATERAL&signature_type=0", nil)
	if err != nil {
		return fmt.Errorf("auth check network error: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 {
		return fmt.Errorf("CLOB auth failed (401) — check POLY_API_KEY/SECRET/PASSPHRASE and that POLY_ADDRESS (%s) matches the wallet that created the API key: %s",
			c.address, string(body))
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("auth check unexpected status %d: %s", resp.StatusCode, string(body))
	}
	c.log.Info("CLOB API credentials verified OK",
		zap.String("address", c.address),
		zap.String("api_key", func() string {
			if len(c.apiKey) > 8 {
				return c.apiKey[:8] + "..."
			}
			return c.apiKey
		}()),
		zap.ByteString("response", body),
	)
	return nil
}

// decodeAPISecret tries multiple base64 variants used by different Polymarket SDK versions.
// Returns the decoded bytes and a short label indicating which variant matched (for debug logs).
// Polymarket's py-clob-client uses urlsafe_b64decode; the secret may or may not have padding.
func decodeAPISecret(secret string) ([]byte, string) {
	// 1. URL-safe with existing padding
	if b, err := base64.URLEncoding.DecodeString(secret); err == nil {
		return b, "url-safe+padding"
	}
	// 2. URL-safe without padding (RawURLEncoding)
	if b, err := base64.RawURLEncoding.DecodeString(secret); err == nil {
		return b, "url-safe-raw"
	}
	// 3. Standard base64 with padding
	if b, err := base64.StdEncoding.DecodeString(secret); err == nil {
		return b, "standard"
	}
	// 4. Fallback: treat as raw bytes (no encoding)
	return []byte(secret), "raw-bytes"
}

// balanceAllowanceResponse is the CLOB /balance-allowance response.
type balanceAllowanceResponse struct {
	Balance     string `json:"balance"`
	Allowance   string `json:"allowance"`
	ProxyWallet string `json:"proxy_wallet"`
}

// FetchProxyWallet calls the CLOB to retrieve the proxy wallet address associated
// with the authenticated EOA (signature_type=1 = POLY_PROXY).
func (c *Client) FetchProxyWallet(ctx context.Context) (string, error) {
	resp, err := c.doWithAuth(ctx, "GET", "/balance-allowance?asset_type=COLLATERAL&signature_type=0", nil)
	if err != nil {
		return "", fmt.Errorf("fetch proxy wallet: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch proxy wallet: HTTP %d: %s", resp.StatusCode, string(body))
	}
	c.log.Debug("balance-allowance response", zap.ByteString("body", body))
	var ba balanceAllowanceResponse
	if err := json.Unmarshal(body, &ba); err != nil {
		return "", fmt.Errorf("parse balance-allowance: %w", err)
	}
	if ba.ProxyWallet == "" {
		return "", fmt.Errorf("proxy_wallet not returned by CLOB (body: %s)", string(body))
	}
	return ba.ProxyWallet, nil
}


// get performs an unauthenticated GET request and returns the response body.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}
