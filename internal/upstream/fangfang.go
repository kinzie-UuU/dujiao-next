package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"

	"github.com/dujiao-next/internal/constants"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/shopspring/decimal"
)

const fangfangOrigin = "https://canboso.com"
const fangfangResponseLimit = 8 << 20

// These stable adapter IDs are not truncated supplier IDs. Never reassign one:
// existing product/SKU mappings persist these values in the local database.
var fangfangReviewedProducts = map[string]uint{
	"6aeedc6cadf360530f5faf80": 1, // Muse 31B, first-login warranty only.
	"6a4b4d7d1827db4ca39bb8be": 2, // ChatGPT Plus Apple Pay Gmail 30 days, full warranty.
}

type fangfangAdapter struct {
	key    string
	client *http.Client
}

var _ Adapter = (*fangfangAdapter)(nil)

func newFangfangAdapter(conn *siteconnectiondomain.Connection) (*fangfangAdapter, error) {
	if conn == nil || strings.TrimSuffix(conn.BaseURL, "/") != fangfangOrigin {
		return nil, errors.New("Fang Fang requires " + fangfangOrigin)
	}
	key := strings.TrimSpace(conn.ApiSecret)
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("Fang Fang API credential is required")
	}
	return &fangfangAdapter{key: key, client: newImageHTTPClient()}, nil
}

// Only documented, read-only endpoints are reachable through this client.
func (a *fangfangAdapter) read(ctx context.Context, endpoint string, out any) error {
	if endpoint != "/balance" && endpoint != "/products" {
		return errors.New("unsupported Fang Fang read endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fangfangOrigin+"/api/v2/telegram-buyer"+endpoint, nil)
	if err != nil {
		return errors.New("invalid Fang Fang request")
	}
	req.Header.Set("x-buyer-key", a.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "UuUAiGC-Supplier-Preflight/1.0")
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("Fang Fang request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Fang Fang HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, fangfangResponseLimit+1))
	if err != nil || len(body) > fangfangResponseLimit {
		return errors.New("invalid Fang Fang response")
	}
	var envelope struct {
		Success  bool   `json:"success"`
		Currency string `json:"walletCurrency"`
	}
	if json.Unmarshal(body, &envelope) != nil || !envelope.Success || envelope.Currency != "USD" {
		return errors.New("invalid Fang Fang Global USD response")
	}
	if json.Unmarshal(body, out) != nil {
		return errors.New("invalid Fang Fang data")
	}
	return nil
}

func fangfangAmount(raw json.Number, allowZero bool) (decimal.Decimal, error) {
	if len(raw.String()) == 0 || len(raw.String()) > 64 {
		return decimal.Zero, errors.New("invalid Fang Fang amount")
	}
	d, err := decimal.NewFromString(raw.String())
	if err != nil || d.Exponent() < -8 || d.Exponent() > 18 || d.IsNegative() || (!allowZero && d.IsZero()) {
		return decimal.Zero, errors.New("invalid Fang Fang amount")
	}
	return d, nil
}

func (a *fangfangAdapter) Ping(ctx context.Context) (*PingResult, error) {
	var response struct {
		Balance json.Number `json:"balance"`
	}
	if err := a.read(ctx, "/balance", &response); err != nil {
		return nil, err
	}
	balance, err := fangfangAmount(response.Balance, true)
	if err != nil {
		return nil, err
	}
	return &PingResult{SiteName: "Fang Fang Global", ProtocolVersion: "buyer-v2-readonly", Balance: balance.String(), Currency: "USD"}, nil
}
func (a *fangfangAdapter) ListCategories(context.Context) (*CategoryListResult, error) {
	return &CategoryListResult{Supported: false}, nil
}

func (a *fangfangAdapter) ListProducts(ctx context.Context, opts ListProductsOpts) (*ProductListResult, error) {
	var response struct {
		Products []struct {
			ID          string `json:"productId"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"productType"`
			Price       struct {
				Amount   json.Number `json:"amount"`
				Currency string      `json:"currency"`
			} `json:"price"`
			Availability struct {
				Available *int `json:"available"`
			} `json:"availability"`
			Requirements json.RawMessage `json:"purchaseRequirements"`
		} `json:"products"`
	}
	if err := a.read(ctx, "/products", &response); err != nil {
		return nil, err
	}
	if response.Products == nil {
		return nil, errors.New("invalid Fang Fang catalog")
	}
	items := make([]UpstreamProduct, 0, len(fangfangReviewedProducts))
	seen := map[uint]bool{}
	for _, p := range response.Products {
		id, ok := fangfangReviewedProducts[p.ID]
		if !ok {
			continue
		}
		price, err := fangfangAmount(p.Price.Amount, false)
		requirements := strings.TrimSpace(string(p.Requirements))
		if err != nil || seen[id] || p.Price.Currency != "USD" || p.Type != "account" || p.Availability.Available == nil || *p.Availability.Available < 0 || strings.TrimSpace(p.Name) == "" || (requirements != "" && requirements != "null" && requirements != "{}") {
			return nil, errors.New("invalid reviewed Fang Fang product")
		}
		seen[id] = true
		stock := constants.ProductStockStatusInStock
		if *p.Availability.Available == 0 {
			stock = constants.ProductStockStatusOutOfStock
		}
		description := html.EscapeString(p.Description)
		items = append(items, UpstreamProduct{ID: id, Title: jsonmap.JSON{"zh-CN": p.Name}, Description: jsonmap.JSON{"zh-CN": description}, Content: jsonmap.JSON{"zh-CN": description}, PriceAmount: price.String(), Currency: "USD", FulfillmentType: constants.FulfillmentTypeAuto, IsActive: true, SKUs: []UpstreamSKU{{ID: id, SKUCode: p.ID, PriceAmount: price.String(), StockStatus: stock, StockQuantity: *p.Availability.Available, IsActive: true}}})
	}
	total := len(items)
	if opts.PageSize > 0 {
		page := opts.Page
		if page < 1 {
			page = 1
		}
		if page-1 > total/opts.PageSize {
			items = []UpstreamProduct{}
		} else {
			items = items[(page-1)*opts.PageSize:]
			if len(items) > opts.PageSize {
				items = items[:opts.PageSize]
			}
		}
	}
	return &ProductListResult{Total: total, Items: items, IncludesInactive: false}, nil
}
func (a *fangfangAdapter) GetProduct(ctx context.Context, id uint) (*UpstreamProduct, error) {
	list, err := a.ListProducts(ctx, ListProductsOpts{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].ID == id {
			return &list.Items[i], nil
		}
	}
	return nil, ErrUpstreamProductUnavailable
}
func (a *fangfangAdapter) CreateOrder(context.Context, CreateUpstreamOrderReq) (*CreateUpstreamOrderResp, error) {
	return &CreateUpstreamOrderResp{OK: false, ErrorCode: "invalid_request", ErrorMessage: "Fang Fang purchasing is not enabled; read-only integration"}, nil
}
func (a *fangfangAdapter) GetOrder(context.Context, uint) (*UpstreamOrderDetail, error) {
	return nil, errors.New("Fang Fang order lookup is not enabled")
}
func (a *fangfangAdapter) CancelOrder(context.Context, uint) error {
	return errors.New("Fang Fang cancellation is not enabled")
}
func (a *fangfangAdapter) DownloadImage(context.Context, string) (string, error) {
	return "", errors.New("Fang Fang image download is not enabled")
}
