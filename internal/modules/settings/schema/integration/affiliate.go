package settingsintegration

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	settingsvalue "github.com/dujiao-next/internal/modules/settings/schema/value"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

const (
	affiliateCommissionRateMin       = 0
	affiliateCommissionRateMax       = 100
	affiliateConfirmDaysMin          = 0
	affiliateConfirmDaysMax          = 3650
	affiliateMinWithdrawAmountMin    = 0
	affiliateWithdrawChannelsMaxSize = 20
	affiliateWithdrawChannelMaxRune  = 50
)

var ErrAffiliateConfigInvalid = errors.New("affiliate config invalid")

const AffiliateRewardModeGPTWallet = "gpt_random_wallet"

// AffiliateSetting 是推广返利设置的 typed representation。
type AffiliateSetting struct {
	AllProducts        bool     `json:"all_products"`
	RewardMode         string   `json:"reward_mode"`
	EligibleProductIDs []uint   `json:"eligible_product_ids"`
	Enabled            bool     `json:"enabled"`
	CommissionRate     float64  `json:"commission_rate"`
	ConfirmDays        int      `json:"confirm_days"`
	MinWithdrawAmount  float64  `json:"min_withdraw_amount"`
	WithdrawChannels   []string `json:"withdraw_channels"`
}

// DefaultAffiliateSetting 返回稳定的推广返利默认设置。
func DefaultAffiliateSetting() AffiliateSetting {
	return NormalizeAffiliateSetting(AffiliateSetting{
		WithdrawChannels: []string{},
	})
}

// NormalizeAffiliateSetting 归一化数值范围和提现渠道。
func NormalizeAffiliateSetting(setting AffiliateSetting) AffiliateSetting {
	setting.RewardMode = strings.TrimSpace(setting.RewardMode)
	ids := make([]uint, 0, len(setting.EligibleProductIDs))
	seen := make(map[uint]bool)
	for _, id := range setting.EligibleProductIDs {
		if id > 0 && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	setting.EligibleProductIDs = ids
	if setting.RewardMode == AffiliateRewardModeGPTWallet {
		setting.CommissionRate = 0
		setting.MinWithdrawAmount = 0
		setting.WithdrawChannels = nil
	}
	setting.CommissionRate = roundAffiliateDecimal(setting.CommissionRate)
	if setting.CommissionRate < affiliateCommissionRateMin {
		setting.CommissionRate = affiliateCommissionRateMin
	}
	if setting.CommissionRate > affiliateCommissionRateMax {
		setting.CommissionRate = affiliateCommissionRateMax
	}
	if setting.ConfirmDays < affiliateConfirmDaysMin {
		setting.ConfirmDays = affiliateConfirmDaysMin
	}
	if setting.ConfirmDays > affiliateConfirmDaysMax {
		setting.ConfirmDays = affiliateConfirmDaysMax
	}
	setting.MinWithdrawAmount = roundAffiliateDecimal(setting.MinWithdrawAmount)
	if setting.MinWithdrawAmount < affiliateMinWithdrawAmountMin {
		setting.MinWithdrawAmount = affiliateMinWithdrawAmountMin
	}
	setting.WithdrawChannels = normalizeAffiliateWithdrawChannels(setting.WithdrawChannels)
	return setting
}

// ValidateAffiliateSetting 保留现有归一化后校验合同。
func ValidateAffiliateSetting(setting AffiliateSetting) error {
	normalized := NormalizeAffiliateSetting(setting)
	if normalized.RewardMode != "" && normalized.RewardMode != AffiliateRewardModeGPTWallet {
		return fmt.Errorf("%w: 未知奖励模式", ErrAffiliateConfigInvalid)
	}
	if normalized.Enabled && normalized.RewardMode == AffiliateRewardModeGPTWallet && !normalized.AllProducts && len(normalized.EligibleProductIDs) == 0 {
		return fmt.Errorf("%w: 请配置参与奖励的商品范围", ErrAffiliateConfigInvalid)
	}
	if normalized.CommissionRate < affiliateCommissionRateMin || normalized.CommissionRate > affiliateCommissionRateMax {
		return fmt.Errorf("%w: 返利比例必须在 0-100 之间", ErrAffiliateConfigInvalid)
	}
	if normalized.ConfirmDays < affiliateConfirmDaysMin || normalized.ConfirmDays > affiliateConfirmDaysMax {
		return fmt.Errorf("%w: 佣金确认天数必须在 0-3650 之间", ErrAffiliateConfigInvalid)
	}
	if normalized.MinWithdrawAmount < affiliateMinWithdrawAmountMin {
		return fmt.Errorf("%w: 最低提现金额不能小于 0", ErrAffiliateConfigInvalid)
	}
	return nil
}

// DecodeAffiliateSetting 从持久化 JSON 解码，并对缺失字段使用 fallback。
func DecodeAffiliateSetting(raw jsonmap.JSON, fallback AffiliateSetting) AffiliateSetting {
	result := fallback
	if value, exists := raw["all_products"]; exists {
		result.AllProducts = settingsvalue.ParseBool(value)
	}
	if value, exists := raw["reward_mode"]; exists {
		result.RewardMode, _ = value.(string)
	}
	if value, exists := raw["eligible_product_ids"]; exists {
		result.EligibleProductIDs = nil
		if data, err := json.Marshal(value); err == nil {
			_ = json.Unmarshal(data, &result.EligibleProductIDs)
		}
	}
	if value, exists := raw["enabled"]; exists {
		result.Enabled = settingsvalue.ParseBool(value)
	}
	if value, exists := raw["commission_rate"]; exists {
		if parsed, err := settingsvalue.ParseFloat(value); err == nil {
			result.CommissionRate = parsed
		}
	}
	if value, exists := raw["confirm_days"]; exists {
		if parsed, err := settingsvalue.ParseInt(value); err == nil {
			result.ConfirmDays = parsed
		}
	}
	if value, exists := raw["min_withdraw_amount"]; exists {
		if parsed, err := settingsvalue.ParseFloat(value); err == nil {
			result.MinWithdrawAmount = parsed
		}
	}
	if value, exists := raw["withdraw_channels"]; exists {
		result.WithdrawChannels = settingsvalue.NormalizeStringList(value)
	}
	return NormalizeAffiliateSetting(result)
}

// EncodeAffiliateSetting 把 typed setting 编码为稳定的持久化 JSON。
func EncodeAffiliateSetting(setting AffiliateSetting) jsonmap.JSON {
	normalized := NormalizeAffiliateSetting(setting)
	return jsonmap.JSON{
		"all_products":         normalized.AllProducts,
		"reward_mode":          normalized.RewardMode,
		"eligible_product_ids": normalized.EligibleProductIDs,
		"enabled":              normalized.Enabled,
		"commission_rate":      normalized.CommissionRate,
		"confirm_days":         normalized.ConfirmDays,
		"min_withdraw_amount":  normalized.MinWithdrawAmount,
		"withdraw_channels":    settingsvalue.CloneStringSlice(normalized.WithdrawChannels),
	}
}

// NormalizeAffiliateSettingJSON 是 Registry 使用的原始 JSON 写入策略。
func NormalizeAffiliateSettingJSON(raw jsonmap.JSON) jsonmap.JSON {
	return EncodeAffiliateSetting(DecodeAffiliateSetting(raw, DefaultAffiliateSetting()))
}

func roundAffiliateDecimal(value float64) float64 {
	return math.Round(value*100) / 100
}

func normalizeAffiliateWithdrawChannels(channels []string) []string {
	if len(channels) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(channels))
	seen := make(map[string]struct{}, len(channels))
	for _, raw := range channels {
		value := settingsvalue.NormalizeTextWithRuneLimit(raw, affiliateWithdrawChannelMaxRune)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) >= affiliateWithdrawChannelsMaxSize {
			break
		}
	}
	return result
}
