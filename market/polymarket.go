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

const (
	gammaAPI = "https://gamma-api.polymarket.com"
	clobAPI  = "https://clob.polymarket.com"
)

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
	NegRisk     bool // true if market uses NegRisk CTF exchange
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
func NewClient(apiKey, apiSecret, passphrase, address string, log *zap.Logger) *Client {
	return &Client{
		http:       &http.Client{Timeout: 10 * time.Second},
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

// RunWatcher polls the market for slugPrefix (e.g. "btc-updown-5m") every 1s
// and publishes state updates.
func RunWatcher(ctx context.Context, c *Client, slugPrefix string, out chan<- State, log *zap.Logger) {
	ticker := time.NewTicker(1 * time.Second)
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
	}, nil
}

type marketInfo struct {
	ConditionID string
	TokenIDUp   string
	TokenIDDown string
	NegRisk     bool
}

// gammaMarket is the Gamma API market object returned in the array response.
// clobTokenIds and outcomes are JSON-encoded string arrays (encoded inside a string).
type gammaMarket struct {
	ConditionID  string `json:"conditionId"`
	ClobTokenIds string `json:"clobTokenIds"`
	Outcomes     string `json:"outcomes"`
	NegRisk      bool   `json:"negRisk"`
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

	info := marketInfo{ConditionID: m.ConditionID, NegRisk: m.NegRisk}
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
func (c *Client) PlaceOrder(ctx context.Context, req OrderRequest) (*OrderResponse, error) {
	body, err := json.Marshal(req)
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
		return nil, fmt.Errorf("CLOB API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var orderResp OrderResponse
	if err := json.Unmarshal(respBody, &orderResp); err != nil {
		return nil, err
	}
	return &orderResp, nil
}

// OrderRequest is the JSON body for POST /order.
// NegRisk must be true for binary CTF markets (UP/DOWN).
type OrderRequest struct {
	Order     SignedOrder `json:"order"`
	Owner     string     `json:"owner"`
	OrderType string     `json:"orderType"` // "FOK", "GTC", "IOC"
	TickSize  string     `json:"tickSize"`  // "0.01" for binary markets
	NegRisk   bool       `json:"negRisk"`   // true for binary outcome markets
}

// SignedOrder is the EIP-712 signed order struct sent to the CLOB.
// Side and SignatureType are sent as strings per Polymarket API convention.
type SignedOrder struct {
	Salt          string `json:"salt"`
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	Taker         string `json:"taker"`
	TokenID       string `json:"tokenId"`
	MakerAmount   string `json:"makerAmount"`
	TakerAmount   string `json:"takerAmount"`
	Expiration    string `json:"expiration"`
	Nonce         string `json:"nonce"`
	FeeRateBps    string `json:"feeRateBps"`
	Side          string `json:"side"`          // "BUY" or "SELL"
	SignatureType string `json:"signatureType"` // "0" = EOA
	Signature     string `json:"signature"`
}

// OrderResponse from CLOB POST /order.
type OrderResponse struct {
	OrderID   string `json:"orderID"`
	Status    string `json:"status"`
	TakerHash string `json:"transactionsHashes,omitempty"`
}

// doWithAuth executes an HMAC-signed HTTP request against the CLOB API.
func (c *Client) doWithAuth(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	msgParts := ts + method + path
	if len(body) > 0 {
		msgParts += string(body)
	}

	// The API secret is base64-encoded; decode it before use as HMAC key.
	secretBytes, err := base64.StdEncoding.DecodeString(c.apiSecret)
	if err != nil {
		// Fallback: use raw bytes if not valid base64
		secretBytes = []byte(c.apiSecret)
	}
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(msgParts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

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
