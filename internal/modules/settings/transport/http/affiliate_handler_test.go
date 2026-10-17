package settingshttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	"github.com/gin-gonic/gin"
)

type affiliateScopeService struct {
	current settingsintegration.AffiliateSetting
	saved   settingsintegration.AffiliateSetting
}

func (s *affiliateScopeService) GetAffiliateSetting() (settingsintegration.AffiliateSetting, error) {
	return s.current, nil
}
func (s *affiliateScopeService) UpdateAffiliateSetting(v settingsintegration.AffiliateSetting) (settingsintegration.AffiliateSetting, error) {
	s.saved = v
	return v, nil
}

func TestAffiliateAdminSavePreservesScopeForOlderClients(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"older-client", `{"enabled":true,"reward_mode":"gpt_random_wallet","confirm_days":0}`, true},
		{"explicit-disable", `{"enabled":true,"reward_mode":"gpt_random_wallet","all_products":false,"eligible_product_ids":[1]}`, false},
		{"explicit-enable", `{"enabled":true,"reward_mode":"gpt_random_wallet","all_products":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &affiliateScopeService{current: settingsintegration.AffiliateSetting{AllProducts: true}}
			router := gin.New()
			router.PUT("/affiliate", NewAffiliateHandler(svc).UpdateAffiliate)
			req := httptest.NewRequest(http.MethodPut, "/affiliate", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || svc.saved.AllProducts != tc.want || !svc.saved.Enabled || svc.saved.RewardMode != settingsintegration.AffiliateRewardModeGPTWallet {
				t.Fatalf("save scope = %+v; response %d %s", svc.saved, rec.Code, rec.Body.String())
			}
		})
	}
}
