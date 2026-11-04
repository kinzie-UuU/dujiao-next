package upstream

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const newzoeOrigin = "https://shop.newzoe.cloud"

type newzoeAdapter struct {
	key, secret string
	client      *http.Client
}

func newNewzoeAdapter(conn *siteconnectiondomain.Connection) (*newzoeAdapter, error) {
	if strings.TrimSuffix(conn.BaseURL, "/") != newzoeOrigin {
		return nil, errors.New("Newzoe requires " + newzoeOrigin)
	}
	if conn.ApiKey == "" || conn.ApiSecret == "" {
		return nil, errors.New("Newzoe personal API credentials are required")
	}
	return &newzoeAdapter{conn.ApiKey, conn.ApiSecret, &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (a *newzoeAdapter) request(ctx context.Context, method, endpoint string, body []byte, idempotencyKey string, out any) error {
	path := "/api/v1" + endpoint
	req, err := http.NewRequestWithContext(ctx, method, newzoeOrigin+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	timestamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), uuid.NewString()
	mac := hmac.New(sha256.New, []byte(a.secret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n", method, path, timestamp, nonce)
	mac.Write(body)
	req.Header.Set("X-Api-Key", a.key)
	req.Header.Set("X-Api-Timestamp", timestamp)
	req.Header.Set("X-Api-Nonce", nonce)
	req.Header.Set("X-Api-Signature", hex.EncodeToString(mac.Sum(nil)))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Only status codes enter logs; the upstream response can contain credentials.
	if resp.StatusCode/100 != 2 {
		return &upstreamHTTPError{Status: resp.StatusCode}
	}
	var envelope struct {
		OK   bool
		Data json.RawMessage
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&envelope); err != nil || !envelope.OK || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("invalid Newzoe response")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return errors.New("invalid Newzoe data")
	}
	return nil
}

func (a *newzoeAdapter) Ping(ctx context.Context) (*PingResult, error) {
	var balance struct{ Balance, Currency string }
	if err := a.request(ctx, http.MethodGet, "/account/balance", nil, "", &balance); err != nil {
		return nil, err
	}
	if _, err := decimal.NewFromString(balance.Balance); err != nil || balance.Currency != "CNY" {
		return nil, errors.New("invalid Newzoe balance")
	}
	return &PingResult{SiteName: "Newzoe", ProtocolVersion: "v1", Balance: balance.Balance, Currency: balance.Currency}, nil
}

func (a *newzoeAdapter) ListCategories(context.Context) (*CategoryListResult, error) {
	return &CategoryListResult{Supported: false}, nil
}

const museAccountTerms = "<p>交付内容包括 Muse 账号、密码、双重验证（2FA）信息和邮箱取件链接。请在购买后 1 小时内首次登录。上游仅保障首次登录，建议当天用完额度。</p>"

var newzoeProductTerms = map[uint]string{
	3:  "<p>用于 Codex 的美国实体手机号单次验证。付款后自动交付接码链接，需打开链接获取号码和验证码。每份仅可成功接收一次验证码；未收到验证码不扣次数。无法接收时，可在激活 1.5 分钟后取消并重新兑换号码；收到验证码后次数即被消耗。此商品不承诺其他平台、其他国家或长期号码使用。</p>",
	21: museAccountTerms,
	22: museAccountTerms,
	24: "<p>在你自己的多邻国账号上使用激活码，可开通 12 个月的 Super 会员，无需绑定银行卡。上游标注的质保期为 5 天，激活后即结束。</p>",
}

func (a *newzoeAdapter) ListProducts(ctx context.Context, opts ListProductsOpts) (*ProductListResult, error) {
	var catalog struct {
		Products []struct {
			ID                             uint
			Name, Description, Price, Type string
			Stock                          *int
			MaxQuantity                    int               `json:"max_quantity"`
			InputFields                    []json.RawMessage `json:"input_fields"`
		}
	}
	if err := a.request(ctx, http.MethodGet, "/products", nil, "", &catalog); err != nil {
		return nil, err
	}
	if catalog.Products == nil {
		return nil, errors.New("invalid Newzoe catalog")
	}
	items := []UpstreamProduct{}
	active := make(map[uint]bool, len(newzoeProductTerms))
	for _, p := range catalog.Products {
		if newzoeProductTerms[p.ID] == "" || p.Type != "automatic" || len(p.InputFields) > 0 {
			continue
		}
		price, err := decimal.NewFromString(p.Price)
		if err != nil || !price.IsPositive() || p.Stock == nil || *p.Stock < 0 || strings.TrimSpace(p.Name) == "" {
			return nil, errors.New("invalid Newzoe product")
		}
		active[p.ID] = true
		qty := *p.Stock
		stock := constants.ProductStockStatusInStock
		if qty == 0 {
			stock = constants.ProductStockStatusOutOfStock
		}
		items = append(items, UpstreamProduct{
			ID: p.ID, Title: jsonmap.JSON{"zh-CN": p.Name},
			Description: jsonmap.JSON{"zh-CN": html.EscapeString(p.Description)},
			Content:     jsonmap.JSON{"zh-CN": newzoeProductTerms[p.ID]},
			PriceAmount: p.Price, Currency: "CNY", MaxPurchaseQuantity: p.MaxQuantity,
			FulfillmentType: constants.FulfillmentTypeAuto, IsActive: true,
			SKUs: []UpstreamSKU{{
				ID: p.ID, SKUCode: fmt.Sprintf("NEWZOE-%d", p.ID),
				PriceAmount: p.Price, IsActive: true, StockQuantity: qty, StockStatus: stock,
			}},
		})
	}
	// Newzoe lists the complete on-sale catalog. In synchronization mode,
	// represent missing supported products explicitly so stale stock is hidden.
	if opts.IncludeInactive {
		for _, id := range []uint{3, 21, 22, 24} {
			if !active[id] {
				items = append(items, UpstreamProduct{ID: id, IsActive: false})
			}
		}
	}
	total := len(items)
	if opts.PageSize > 0 && opts.Page > 1 {
		if opts.Page-1 > total/opts.PageSize {
			items = nil
		} else {
			items = items[(opts.Page-1)*opts.PageSize:]
		}
	}
	if opts.PageSize > 0 && len(items) > opts.PageSize {
		items = items[:opts.PageSize]
	}
	return &ProductListResult{Items: items, Total: total, IncludesInactive: opts.IncludeInactive}, nil
}

func (a *newzoeAdapter) GetProduct(ctx context.Context, id uint) (*UpstreamProduct, error) {
	list, err := a.ListProducts(ctx, ListProductsOpts{})
	if err != nil {
		return nil, err
	}
	for _, p := range list.Items {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, ErrUpstreamProductUnavailable
}

var newzoeOrderNumber = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type newzoeOrderResponse struct {
	Order struct {
		ID, Status, Amount, Currency string
		Quantity                     int
		PaymentReceived              bool `json:"payment_received"`
	}
}

func (a *newzoeAdapter) CreateOrder(ctx context.Context, input CreateUpstreamOrderReq) (*CreateUpstreamOrderResp, error) {
	if newzoeProductTerms[input.SKUID] == "" || input.Quantity < 1 || !newzoeOrderNumber.MatchString(input.DownstreamOrderNo) {
		return &CreateUpstreamOrderResp{ErrorCode: "invalid_request"}, nil
	}
	body, err := json.Marshal(struct {
		ProductID     uint   `json:"product_id"`
		Quantity      int    `json:"quantity"`
		PaymentMethod string `json:"payment_method"`
	}{input.SKUID, input.Quantity, "balance"})
	if err != nil {
		return nil, err
	}
	var result newzoeOrderResponse
	if err := a.request(ctx, http.MethodPost, "/orders", body, input.DownstreamOrderNo, &result); err != nil {
		var upstreamErr *upstreamHTTPError
		if errors.As(err, &upstreamErr) && upstreamErr.Status >= 400 && upstreamErr.Status < 500 && upstreamErr.Status != 408 && upstreamErr.Status != 429 {
			return &CreateUpstreamOrderResp{ErrorCode: "invalid_request", ErrorMessage: fmt.Sprintf("Newzoe rejected purchase (HTTP %d)", upstreamErr.Status)}, nil
		}
		return nil, err
	}
	order := result.Order
	if !newzoeOrderNumber.MatchString(order.ID) {
		return nil, errors.New("missing or invalid Newzoe order number")
	}
	return &CreateUpstreamOrderResp{OK: true, OrderNo: order.ID, Amount: order.Amount, Currency: order.Currency, Status: order.Status}, nil
}

func (a *newzoeAdapter) GetOrderByNumber(ctx context.Context, orderNo string) (*UpstreamOrderDetail, error) {
	if !newzoeOrderNumber.MatchString(orderNo) {
		return nil, errors.New("invalid Newzoe order number")
	}
	path := "/orders/" + orderNo
	var result newzoeOrderResponse
	if err := a.request(ctx, http.MethodGet, path, nil, "", &result); err != nil {
		return nil, err
	}
	order := result.Order
	if order.ID != orderNo {
		return nil, errors.New("Newzoe order number mismatch")
	}
	detail := &UpstreamOrderDetail{OrderNo: order.ID, Amount: order.Amount, Currency: order.Currency, Status: constants.OrderStatusFulfilling}
	switch order.Status {
	case "pending", "processing":
		return detail, nil
	case "failure", "expired", "abnormal", "wait_pay":
		detail.Status = "failed"
		return detail, nil
	case "completed":
		if !order.PaymentReceived {
			return nil, errors.New("Newzoe completed order lacks payment confirmation")
		}
	default:
		return nil, errors.New("unknown Newzoe order status")
	}
	var delivered struct {
		Delivery struct {
			Available    bool
			Status, Type string
			Items        []string
		}
	}
	if err := a.request(ctx, http.MethodGet, path+"/delivery", nil, "", &delivered); err != nil {
		return nil, err
	}
	d := delivered.Delivery
	if !d.Available {
		return detail, nil
	}
	if d.Status != "completed" || d.Type != "automatic" || order.Quantity < 1 || len(d.Items) != order.Quantity {
		return nil, errors.New("incomplete Newzoe delivery")
	}
	for _, item := range d.Items {
		if strings.TrimSpace(item) == "" {
			return nil, errors.New("empty Newzoe delivery item")
		}
	}
	detail.Status = constants.OrderStatusDelivered
	detail.Fulfillment = &UpstreamFulfillment{Type: constants.FulfillmentTypeAuto, Status: constants.FulfillmentStatusDelivered, Payload: strings.Join(d.Items, "\n")}
	return detail, nil
}

func (*newzoeAdapter) GetOrder(context.Context, uint) (*UpstreamOrderDetail, error) {
	return nil, errors.New("Newzoe requires the string order number")
}
func (*newzoeAdapter) CancelOrder(context.Context, uint) error {
	return errors.New("Newzoe does not support cancellation")
}
func (*newzoeAdapter) DownloadImage(context.Context, string) (string, error) {
	return "", errors.New("Newzoe catalog does not expose images")
}
