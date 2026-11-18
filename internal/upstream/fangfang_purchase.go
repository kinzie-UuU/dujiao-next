package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/shopspring/decimal"
)

// FangfangPurchaseClient is deliberately separate from the read-only Adapter.
// The caller must durably reserve the idempotency key and persist a submitting
// state BEFORE calling PurchaseOnce. Never automatically retry an uncertain result.
type FangfangPurchaseClient struct{ adapter *fangfangAdapter }
type FangfangPurchaseRequest struct {
	ProductID       string
	Quantity        int
	IdempotencyKey  string
	PreflightMaxUSD string
}

// Accounts contain delivery secrets. Persist securely; never log this result.
type FangfangPurchaseResult struct {
	// RawResponse can contain credentials on any HTTP status. The caller must
	// encrypt and durably persist it before interpreting the outcome or retrying.
	RawResponse   []byte `json:"-"`
	HTTPStatus    int
	OrderCode     string
	ProductID     string
	Quantity      int
	FinalQuantity int
	AmountUSD     string
	Accounts      []json.RawMessage `json:"-"`
}
type FangfangPurchaseError struct {
	Kind      string
	OrderCode string
}

func (e *FangfangPurchaseError) Error() string { return "Fang Fang purchase " + e.Kind }

var fangfangPurchaseToken = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var fangfangOrderCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func NewFangfangPurchaseClient(conn *siteconnectiondomain.Connection) (*FangfangPurchaseClient, error) {
	a, err := newFangfangAdapter(conn)
	if err != nil {
		return nil, err
	}
	return &FangfangPurchaseClient{adapter: a}, nil
}

// PurchaseOnce performs read-only USD/stock/price preflight, then at most one
// POST. The upstream contract has no atomic price cap: price changes between GET
// and POST cannot be prevented. An over-cap charge is uncertain, never fulfilled.
// The supplier documents replay with the same key, but no retention guarantee;
// orders history cannot recover credentials or search an idempotency key.
func (c *FangfangPurchaseClient) PurchaseOnce(ctx context.Context, in FangfangPurchaseRequest) (*FangfangPurchaseResult, error) {
	fail := func(kind string) (*FangfangPurchaseResult, error) { return nil, &FangfangPurchaseError{Kind: kind} }
	id, ok := fangfangReviewedProducts[in.ProductID]
	cap, err := fangfangAmount(json.Number(in.PreflightMaxUSD), false)
	if c == nil || c.adapter == nil || !ok || in.Quantity < 1 || in.Quantity > 1000 || !fangfangPurchaseToken.MatchString(in.IdempotencyKey) || err != nil {
		return fail("invalid_request")
	}
	p, err := c.adapter.GetProduct(ctx, id)
	if err != nil || p == nil || p.Currency != "USD" || len(p.SKUs) != 1 || p.SKUs[0].StockQuantity < in.Quantity {
		return fail("rejected")
	}
	unit, err := decimal.NewFromString(p.PriceAmount)
	if err != nil || !unit.IsPositive() || unit.Mul(decimal.NewFromInt(int64(in.Quantity))).GreaterThan(cap) {
		return fail("rejected")
	}
	if ctx.Err() != nil {
		return fail("rejected")
	}
	// Body.key is required by the published PurchaseRequest schema. It must never
	// be logged or added to a URL. GET authentication does not establish POST support.
	body, _ := json.Marshal(struct {
		Key       string `json:"key"`
		ProductID string `json:"product_id"`
		Quantity  int    `json:"quantity"`
	}{c.adapter.key, in.ProductID, in.Quantity})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fangfangOrigin+"/api/v2/telegram-buyer/purchase", bytes.NewReader(body))
	if err != nil {
		return fail("invalid_request")
	}
	// Go's Transport may replay POSTs with Idempotency-Key and GetBody on reused
	// connections. Explicitly remove replay support, even for an upstream retry.
	req.GetBody = nil
	req.Header.Set("Idempotency-Key", in.IdempotencyKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.adapter.client.Do(req)
	if err != nil {
		return fail("uncertain")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, fangfangResponseLimit+1))
	result := &FangfangPurchaseResult{RawResponse: raw, HTTPStatus: resp.StatusCode}
	if err != nil || len(raw) > fangfangResponseLimit {
		return result, &FangfangPurchaseError{Kind: "uncertain"}
	}
	var out struct {
		Success *bool `json:"success"`
		Order   struct {
			Code      string `json:"orderCode"`
			Status    string `json:"status"`
			ProductID string `json:"productId"`
			Type      string `json:"productType"`
			Quantity  int    `json:"quantity"`
			Bonus     *int   `json:"bonusQuantity"`
			Final     int    `json:"finalQuantity"`
		} `json:"order"`
		Payment struct {
			Amount   json.Number `json:"amount"`
			Currency string      `json:"currency"`
		} `json:"payment"`
		Delivery struct {
			Accounts []json.RawMessage `json:"accounts"`
		} `json:"delivery"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Success == nil {
		return result, &FangfangPurchaseError{Kind: "uncertain"}
	}
	// A POST response does not by itself prove no earlier request was charged.
	// Treat every non-success as uncertain; reconciliation decides next steps.
	uncertain := &FangfangPurchaseError{Kind: "uncertain"}
	if fangfangOrderCode.MatchString(out.Order.Code) {
		uncertain.OrderCode = out.Order.Code
	}
	result.OrderCode = uncertain.OrderCode
	result.Accounts = out.Delivery.Accounts
	amount, err := fangfangAmount(out.Payment.Amount, false)
	if resp.StatusCode != 200 || !*out.Success || uncertain.OrderCode == "" || out.Order.Status != "completed" || out.Order.ProductID != in.ProductID || out.Order.Type != "account" || out.Order.Quantity != in.Quantity || out.Order.Bonus == nil || *out.Order.Bonus < 0 || *out.Order.Bonus > 1000 || out.Order.Final != in.Quantity+*out.Order.Bonus || len(out.Delivery.Accounts) != out.Order.Final || out.Payment.Currency != "USD" || err != nil || amount.GreaterThan(cap) {
		return result, uncertain
	}
	seen := map[string]bool{}
	for _, account := range out.Delivery.Accounts {
		var a struct {
			User     string `json:"user"`
			Password string `json:"password"`
		}
		if json.Unmarshal(account, &a) != nil || strings.TrimSpace(a.User) == "" || strings.TrimSpace(a.Password) == "" || seen[strings.ToLower(strings.TrimSpace(a.User))] {
			return result, uncertain
		}
		seen[strings.ToLower(strings.TrimSpace(a.User))] = true
	}
	result.ProductID, result.Quantity, result.FinalQuantity, result.AmountUSD = in.ProductID, in.Quantity, out.Order.Final, amount.String()
	return result, nil
}
