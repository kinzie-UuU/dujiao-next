package gormstore

import (
	"encoding/json"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
	"github.com/shopspring/decimal"
)

var totpSecret = regexp.MustCompile(`^[A-Z2-7]{16,64}$`)

// parseVerifiedDeliveryPayload checks the immutable purchase body against the
// customer-facing Muse/Plus delivery contract and returns the shop payload.
// Unknown or incomplete account fields fail closed.
func parseVerifiedDeliveryPayload(product backupstate.Product, upstreamProductID string, quantity int, preflightMaxUSD string, raw []byte) (string, error) {
	if quantity != 1 || len(raw) == 0 || strings.TrimSpace(preflightMaxUSD) != preflightMaxUSD || preflightMaxUSD == "" {
		return "", errDeliveryPolicy
	}
	limit, err := decimal.NewFromString(preflightMaxUSD)
	if err != nil || !limit.IsPositive() {
		return "", errDeliveryPolicy
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
	if json.Unmarshal(raw, &out) != nil || out.Success == nil || !*out.Success || out.Order.Status != "completed" ||
		out.Order.ProductID != upstreamProductID || out.Order.Type != "account" || out.Order.Quantity != quantity ||
		out.Order.Bonus == nil || *out.Order.Bonus < 0 || *out.Order.Bonus > 1000 || out.Order.Final != quantity+*out.Order.Bonus ||
		len(out.Delivery.Accounts) != out.Order.Final || out.Payment.Currency != "USD" || strings.TrimSpace(out.Order.Code) == "" {
		return "", errDeliveryPolicy
	}
	amount, err := decimal.NewFromString(string(out.Payment.Amount))
	if err != nil || amount.GreaterThan(limit) {
		return "", errDeliveryPolicy
	}
	lines := make([]string, 0, len(out.Delivery.Accounts))
	seen := map[string]bool{}
	for _, account := range out.Delivery.Accounts {
		line, key, err := formatAccountLine(product, account)
		if err != nil || key == "" || seen[key] {
			return "", errDeliveryPolicy
		}
		seen[key] = true
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", errDeliveryPolicy
	}
	return strings.Join(lines, "\n"), nil
}

func formatAccountLine(product backupstate.Product, raw json.RawMessage) (string, string, error) {
	var a struct {
		User        string `json:"user"`
		Password    string `json:"password"`
		VerifyEmail string `json:"verifyEmail"`
		ExpiryText  string `json:"expiryText"`
		OtherInfo   string `json:"otherInfo"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return "", "", errDeliveryPolicy
	}
	user := strings.TrimSpace(a.User)
	password := strings.TrimSpace(a.Password)
	verify := strings.TrimSpace(a.VerifyEmail)
	other := strings.TrimSpace(a.OtherInfo)
	expiry := strings.TrimSpace(a.ExpiryText)
	if user == "" || password == "" || strings.Contains(user, "----") || strings.Contains(password, "----") ||
		strings.Contains(verify, "----") || strings.Contains(other, "----") || strings.Contains(expiry, "----") {
		return "", "", errDeliveryPolicy
	}
	if _, err := mail.ParseAddress(user); err != nil {
		return "", "", errDeliveryPolicy
	}
	key := strings.ToLower(user)
	switch product {
	case backupstate.Muse:
		link := password
		if !isHTTPURL(link) {
			if !isHTTPURL(verify) {
				return "", "", errDeliveryPolicy
			}
			link = verify
		}
		// Muse customer contract: email | mailbox access link.
		return user + "----" + link, key, nil
	case backupstate.Gmail:
		// Supplier contract: Gmail, password, recovery email and/or a TOTP secret.
		if !strings.HasSuffix(strings.ToLower(user), "@gmail.com") || strings.ContainsAny(password+verify+other+expiry, "\r\n") {
			return "", "", errDeliveryPolicy
		}
		twoFA := strings.ToUpper(strings.ReplaceAll(other, " ", ""))
		if twoFA != "" && !totpSecret.MatchString(twoFA) {
			return "", "", errDeliveryPolicy
		}
		if verify != "" {
			if address, err := mail.ParseAddress(verify); err != nil || address.Address != verify {
				// The supplier can use verifyEmail for the optional 2FA field.
				candidate := strings.ToUpper(strings.ReplaceAll(verify, " ", ""))
				if !totpSecret.MatchString(candidate) || (twoFA != "" && twoFA != candidate) {
					return "", "", errDeliveryPolicy
				}
				twoFA, verify = candidate, ""
			}
		}
		if verify == "" && twoFA == "" {
			return "", "", errDeliveryPolicy
		}
		line := user + "----" + password + "----" + verify
		if twoFA != "" {
			line += "----" + twoFA
		}
		return line, key, nil
	case backupstate.Plus:
		twoFA := other
		if twoFA == "" || !totpSecret.MatchString(strings.ToUpper(strings.ReplaceAll(twoFA, " ", ""))) {
			return "", "", errDeliveryPolicy
		}
		twoFA = strings.ToUpper(strings.ReplaceAll(twoFA, " ", ""))
		// Plus customer contract: email | password | 2FA; expiry when supplier provides it.
		line := user + "----" + password + "----" + twoFA
		if expiry != "" {
			line += "----" + expiry
		}
		return line, key, nil
	default:
		return "", "", errDeliveryPolicy
	}
}

func isHTTPURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
