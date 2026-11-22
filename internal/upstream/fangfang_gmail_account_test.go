package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const packedGmailUser = "aged.account@gmail.com|syntheticPass|recovery@example.net|JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP\t\t\t\t\t\tThành phố Huế"
const packedGmailExpected = "aged.account@gmail.com----syntheticPass----recovery@example.net----JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP Thành phố Huế"

func packedGmailAccount(user string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"user": user, "password": "", "verifyEmail": nil, "otherInfo": nil, "expiryText": nil})
	return raw
}
func TestFangfangPackedGmailPreservesRecoveryTOTPAndNotes(t *testing.T) {
	raw := packedGmailAccount(packedGmailUser)
	got, key, err := FormatFangfangGmailAccount(raw)
	if err != nil || got != packedGmailExpected || key != "aged.account@gmail.com" {
		t.Fatalf("packed Gmail validation failed: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(packedGmailUser, "@gmail.com", "@example.com", 1),
		strings.Replace(packedGmailUser, "syntheticPass", "", 1),
		strings.Replace(packedGmailUser, "recovery@example.net", "not-a-recovery", 1),
		strings.Replace(packedGmailUser, "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", "not-totp", 1),
		packedGmailUser + "\nsecond-line", packedGmailUser + "\r", packedGmailUser + "|extra",
	} {
		if _, _, err := FormatFangfangGmailAccount(packedGmailAccount(bad)); err == nil {
			t.Fatal("malformed packed account accepted")
		}
	}
	ambiguous, _ := json.Marshal(map[string]any{"user": packedGmailUser, "password": "different"})
	if _, _, err := FormatFangfangGmailAccount(ambiguous); err == nil {
		t.Fatal("ambiguous credentials accepted")
	}
}

func TestFangfangPurchasePackedGmailUsesSharedValidation(t *testing.T) {
	for _, tc := range []struct {
		name, product string
		accounts      []json.RawMessage
		wantOK        bool
	}{
		{"gmail", FangfangGmailProductID, []json.RawMessage{packedGmailAccount(packedGmailUser)}, true},
		{"duplicate gmail", FangfangGmailProductID, []json.RawMessage{packedGmailAccount(packedGmailUser), packedGmailAccount(strings.Replace(packedGmailUser, "aged.account", "AGED.ACCOUNT", 1))}, false},
		{"invalid gmail", FangfangGmailProductID, []json.RawMessage{packedGmailAccount(packedGmailUser + "\n")}, false},
		{"plus unchanged", "6a4b4d7d1827db4ca39bb8be", []json.RawMessage{packedGmailAccount(packedGmailUser)}, false},
		{"muse unchanged", "6aeedc6cadf360530f5faf80", []json.RawMessage{packedGmailAccount(packedGmailUser)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := len(tc.accounts)
			response, _ := json.Marshal(map[string]any{"success": true, "order": map[string]any{"orderCode": "ORDER_PACKED", "status": "completed", "productId": tc.product, "productType": "account", "quantity": n, "bonusQuantity": 0, "finalQuantity": n}, "payment": map[string]any{"amount": 1.49, "currency": "USD"}, "delivery": map[string]any{"accounts": tc.accounts}})
			calls := 0
			adapter := fangfangTestClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return fangfangTestResponse(200, fangfangCatalog(`{"productId":"`+tc.product+`","name":"synthetic account","description":"synthetic","productType":"account","price":{"amount":1.49,"currency":"USD"},"availability":{"available":470}}`)), nil
				}
				calls++
				return fangfangTestResponse(200, string(response)), nil
			})
			out, err := (&FangfangPurchaseClient{adapter: adapter}).PurchaseOnce(context.Background(), FangfangPurchaseRequest{ProductID: tc.product, Quantity: n, IdempotencyKey: "packed-test-once", PreflightMaxUSD: "3.00"})
			if calls != 1 || out == nil || string(out.RawResponse) != string(response) {
				t.Fatal("purchase retried or raw receipt changed")
			}
			if tc.wantOK {
				if err != nil {
					t.Fatalf("valid packed Gmail was marked uncertain: %v", err)
				}
			} else {
				fangfangRequirePurchaseKind(t, err, "uncertain")
			}
		})
	}
}
