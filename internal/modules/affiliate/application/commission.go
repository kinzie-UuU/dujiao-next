package application

import (
	"crypto/rand"
	"math/big"
	"strconv"
	"strings"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"

	affiliatecontract "github.com/dujiao-next/internal/modules/affiliate/contract"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/shopspring/decimal"
)

// HandleOrderPaid 处理订单支付成功后的佣金生成
func (s *Service) HandleOrderPaid(orderID uint) error {
	if orderID == 0 || s.repo == nil || s.orderRepo == nil {
		return nil
	}
	setting, err := s.settings.GetAffiliateSetting()
	if err != nil {
		return err
	}
	walletReward := setting.RewardMode == settingsintegration.AffiliateRewardModeGPTWallet
	if !setting.Enabled || (!walletReward && setting.CommissionRate <= 0) {
		return nil
	}
	if walletReward {
		return s.repo.WithinTransaction(func(tx affiliatecontract.Store) error {
			order, err := tx.GetRewardOrderForUpdate(orderID)
			if err != nil {
				return err
			}
			transactional := *s
			transactional.repo = tx
			return transactional.handleOrderPaid(order, setting)
		})
	}

	order, err := s.orderRepo.GetByID(orderID)
	if err != nil {
		return err
	}
	return s.handleOrderPaid(order, setting)
}

func (s *Service) handleOrderPaid(order *orderdomain.Order, setting settingsintegration.AffiliateSetting) error {
	walletReward := setting.RewardMode == settingsintegration.AffiliateRewardModeGPTWallet
	// Delayed payment retries must not create rewards after refund or cancellation.
	if order == nil || order.Status == constants.OrderStatusRefunded || order.Status == constants.OrderStatusCanceled {
		return nil
	}
	if walletReward {
		for _, child := range order.Children {
			if child.RefundedAmount.Decimal.IsPositive() || child.Status == constants.OrderStatusRefunded || child.Status == constants.OrderStatusPartiallyRefunded {
				return nil
			}
		}
	}
	if walletReward && (order.ParentID != nil || order.PaidAt == nil ||
		!strings.EqualFold(order.Currency, "CNY") || order.RefundedAmount.Decimal.IsPositive() ||
		order.Status == constants.OrderStatusPartiallyRefunded || order.Status == constants.OrderStatusRefunded || order.Status == constants.OrderStatusCanceled) {
		return nil
	}
	profile, err := s.resolveAffiliateProfileForOrder(order)
	if err != nil {
		return err
	}
	if profile == nil {
		return nil
	}
	if strings.TrimSpace(profile.Status) != constants.AffiliateProfileStatusActive {
		return nil
	}
	if order.UserID > 0 && profile.UserID == order.UserID {
		return nil
	}

	commissionType := constants.AffiliateCommissionTypeOrder
	existing, err := s.repo.GetCommissionByOrderAndProfile(order.ID, profile.ID, commissionType)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	// Mode changes and repeated callbacks must never create a second reward.
	rewardExisting, err := s.repo.GetCommissionByOrderAndProfile(order.ID, profile.ID, "gpt_reward")
	if err != nil {
		return err
	}
	if rewardExisting != nil {
		return nil
	}
	if walletReward {
		commissionType = "gpt_reward"
	}

	var baseAmount decimal.Decimal
	var rewardAmounts, rewardTotals map[string]string
	if walletReward {
		baseAmount, rewardAmounts, rewardTotals, err = s.rewardOrderSnapshot(order, setting)
	} else {
		baseAmount, err = s.calculateCommissionBaseAmountForSetting(order, setting)
	}
	if err != nil {
		return err
	}
	if baseAmount.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	rate := decimal.NewFromFloat(setting.CommissionRate).Round(2)
	commissionAmount := baseAmount.Mul(rate).Div(decimal.NewFromInt(100)).Round(2)
	if walletReward {
		// Keep the integer reward within the eligible net spend for low-value orders.
		maxReward := int64(5)
		if baseAmount.LessThan(decimal.NewFromInt(5)) {
			maxReward = baseAmount.IntPart()
		}
		if maxReward < 1 {
			return nil
		}
		draw, drawErr := rand.Int(rand.Reader, big.NewInt(maxReward))
		if drawErr != nil {
			return drawErr
		}
		commissionAmount = decimal.NewFromInt(draw.Int64() + 1)
		rate = decimal.Zero
	}
	if commissionAmount.LessThanOrEqual(decimal.Zero) {
		return nil
	}

	paidAt := time.Now()
	if order.PaidAt != nil {
		paidAt = *order.PaidAt
	}
	status := constants.AffiliateCommissionStatusPendingConfirm
	var confirmAt *time.Time
	var availableAt *time.Time
	if setting.ConfirmDays <= 0 {
		status = constants.AffiliateCommissionStatusAvailable
		availableAt = &paidAt
	} else {
		t := paidAt.Add(time.Duration(setting.ConfirmDays) * 24 * time.Hour)
		confirmAt = &t
	}

	commission := &affiliatedomain.Commission{
		AffiliateProfileID: profile.ID,
		OrderID:            order.ID,
		CommissionType:     commissionType,
		BaseAmount:         money.FromDecimal(baseAmount),
		RatePercent:        money.FromDecimal(rate),
		CommissionAmount:   money.FromDecimal(commissionAmount),
		Status:             status,
		ConfirmAt:          confirmAt,
		AvailableAt:        availableAt,
	}
	if walletReward {
		commission.RewardOrderAmounts, commission.RewardOrderTotals = rewardAmounts, rewardTotals
	}
	return s.repo.CreateOrderCommission(commission)
}

// ConfirmDueCommissions 将到期佣金转可提现
func (s *Service) ConfirmDueCommissions(now time.Time) error {
	if s.repo == nil {
		return nil
	}
	_, err := s.repo.MarkPendingCommissionsAvailable(now, now)
	return err
}

// HandleOrderCanceled 处理订单取消/退款后的佣金逆向
func (s *Service) HandleOrderCanceled(orderID uint, reason string) error {
	if orderID == 0 || s.repo == nil {
		return nil
	}
	rows, err := s.repo.ListCommissionsByOrder(orderID, []string{
		constants.AffiliateCommissionStatusPendingConfirm,
		constants.AffiliateCommissionStatusAvailable,
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	now := time.Now()
	reasonText := strings.TrimSpace(reason)
	if reasonText == "" {
		reasonText = "order_canceled"
	}
	for i := range rows {
		item := rows[i]
		if item.WithdrawRequestID != nil {
			// 已进入提现流程，按业务规则不影响用户提现。
			continue
		}
		item.Status = constants.AffiliateCommissionStatusRejected
		item.InvalidReason = reasonText
		item.UpdatedAt = now
		if err := s.repo.UpdateCommission(&item); err != nil {
			return err
		}
	}
	return nil
}

// HandleOrderRefunded 使用调用方提供的事务 Store 处理退款后的佣金回滚。
func (s *Service) HandleOrderRefunded(
	repoTx affiliatecontract.Store,
	order *orderdomain.Order,
	refundDelta decimal.Decimal,
	refundedBefore decimal.Decimal,
	reason string,
	sourceOrderID ...uint,
) error {
	if repoTx == nil || order == nil || order.ID == 0 {
		return nil
	}
	delta := refundDelta.Round(2)
	if delta.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	totalAmount := order.TotalAmount.Decimal.Round(2)
	if totalAmount.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	before := refundedBefore.Round(2)
	if before.LessThan(decimal.Zero) {
		before = decimal.Zero
	}
	if before.GreaterThan(totalAmount) {
		before = totalAmount
	}
	remaining := totalAmount.Sub(before).Round(2)
	if remaining.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	if delta.GreaterThan(remaining) {
		delta = remaining
	}

	rows, err := repoTx.ListCommissionsByOrderForUpdate(order.ID, []string{
		constants.AffiliateCommissionStatusPendingConfirm,
		constants.AffiliateCommissionStatusAvailable,
		"wallet_credited",
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	now := time.Now()
	reasonText := strings.TrimSpace(reason)
	if reasonText == "" {
		reasonText = "order_refunded"
	}
	for i := range rows {
		item := rows[i]

		currentCommission := item.CommissionAmount.Decimal.Round(2)
		if currentCommission.LessThanOrEqual(decimal.Zero) {
			item.Status = constants.AffiliateCommissionStatusRejected
			item.InvalidReason = reasonText
			item.ConfirmAt = nil
			item.AvailableAt = nil
			item.UpdatedAt = now
			if err := repoTx.UpdateCommission(&item); err != nil {
				return err
			}
			continue
		}

		// 按“本次退款金额 / 当前剩余未退款金额”比例扣减当前佣金，避免多次退款时重复放大扣减。
		currentBase := item.BaseAmount.Decimal.Round(2)
		baseDeduct := currentBase.Mul(delta).Div(remaining).Round(2)
		deduct := currentCommission.Mul(delta).Div(remaining).Round(2)
		if item.CommissionType == "gpt_reward" && len(item.RewardOrderAmounts) > 0 {
			sourceID := order.ID
			if len(sourceOrderID) > 0 && sourceOrderID[0] > 0 {
				sourceID = sourceOrderID[0]
			}
			key := strconv.FormatUint(uint64(sourceID), 10)
			eligible, e1 := decimal.NewFromString(item.RewardOrderAmounts[key])
			sourceTotal, e2 := decimal.NewFromString(item.RewardOrderTotals[key])
			if e1 != nil || e2 != nil || !sourceTotal.IsPositive() {
				return ErrNotFound
			}
			if sourceID != order.ID {
				baseDeduct = delta.Mul(eligible).Div(sourceTotal).Round(2)
			}
			if baseDeduct.GreaterThan(currentBase) {
				baseDeduct = currentBase
			}
			if !baseDeduct.IsPositive() || !currentBase.IsPositive() {
				continue
			}
			deduct = currentCommission.Mul(baseDeduct).Div(currentBase).Round(2)
			if deduct.GreaterThan(currentCommission) {
				deduct = currentCommission
			}
		}
		if item.Status == "wallet_credited" && deduct.IsPositive() {
			profile, err := repoTx.GetProfileByID(item.AffiliateProfileID)
			if err != nil {
				return err
			}
			if profile == nil {
				return ErrNotFound
			}
			reference := "affiliate_reward_refund:" + strconv.FormatUint(uint64(item.ID), 10) + ":" + before.Add(delta).StringFixed(2)
			if err := repoTx.AdjustRewardWallet(profile.UserID, deduct.Neg(), reference, order.ID); err != nil {
				return err
			}
		}
		nextCommission := currentCommission.Sub(deduct).Round(2)
		if nextCommission.LessThan(decimal.Zero) {
			nextCommission = decimal.Zero
		}
		nextBase := currentBase
		if currentBase.GreaterThan(decimal.Zero) {
			nextBase = currentBase.Sub(baseDeduct).Round(2)
			if nextBase.LessThan(decimal.Zero) {
				nextBase = decimal.Zero
			}
		}

		item.CommissionAmount = money.FromDecimal(nextCommission)
		item.BaseAmount = money.FromDecimal(nextBase)
		item.UpdatedAt = now
		if nextCommission.LessThanOrEqual(decimal.Zero) {
			item.Status = constants.AffiliateCommissionStatusRejected
			item.InvalidReason = reasonText
			item.ConfirmAt = nil
			item.AvailableAt = nil
		}
		if err := repoTx.UpdateCommission(&item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveAffiliateProfileForOrder(order *orderdomain.Order) (*affiliatedomain.Profile, error) {
	if order == nil || s.repo == nil {
		return nil, nil
	}
	if order.AffiliateProfileID != nil && *order.AffiliateProfileID > 0 {
		return s.repo.GetProfileByID(*order.AffiliateProfileID)
	}
	if strings.TrimSpace(order.AffiliateCode) != "" {
		return s.repo.GetProfileByCode(order.AffiliateCode)
	}
	return nil, nil
}

func (s *Service) calculateCommissionBaseAmount(order *orderdomain.Order) (decimal.Decimal, error) {
	return s.calculateCommissionBaseAmountForSetting(order, settingsintegration.AffiliateSetting{})
}

func (s *Service) calculateCommissionBaseAmountForSetting(order *orderdomain.Order, setting settingsintegration.AffiliateSetting) (decimal.Decimal, error) {
	if setting.RewardMode == settingsintegration.AffiliateRewardModeGPTWallet {
		total, _, _, err := s.rewardOrderSnapshot(order, setting)
		return total, err
	}
	if order == nil || s.productRepo == nil {
		return decimal.Zero, nil
	}
	productIDs := collectAffiliateProductIDs(order)
	if len(productIDs) == 0 {
		return decimal.Zero, nil
	}
	products, err := s.productRepo.ListByIDs(productIDs)
	if err != nil {
		return decimal.Zero, err
	}
	productMap := make(map[uint]productdomain.Product, len(products))
	for _, product := range products {
		productMap[product.ID] = product
	}
	eligible := make(map[uint]bool, len(setting.EligibleProductIDs))
	for _, id := range setting.EligibleProductIDs {
		eligible[id] = true
	}

	targetOrders := order.Children
	if len(targetOrders) == 0 {
		targetOrders = []orderdomain.Order{*order}
	}

	total := decimal.Zero
	for _, current := range targetOrders {
		for _, item := range current.Items {
			product, ok := productMap[item.ProductID]
			if !ok {
				continue
			}
			if setting.RewardMode == settingsintegration.AffiliateRewardModeGPTWallet {
				slug := strings.ToLower(product.Slug)
				if !eligible[product.ID] || !(strings.HasPrefix(slug, "gpt-") || strings.HasPrefix(slug, "chatgpt-")) {
					continue
				}
			} else if !product.IsAffiliateEnabled {
				continue
			}
			payable := item.TotalPrice.Decimal.Sub(item.CouponDiscount.Decimal).Round(2)
			if payable.LessThan(decimal.Zero) {
				payable = decimal.Zero
			}
			total = total.Add(payable).Round(2)
		}
	}
	if setting.RewardMode == settingsintegration.AffiliateRewardModeGPTWallet && total.GreaterThan(order.TotalAmount.Decimal) {
		total = order.TotalAmount.Decimal.Round(2)
	}
	return total, nil
}

// rewardOrderSnapshot freezes the eligible net spend of every checkout order.
func (s *Service) rewardOrderSnapshot(order *orderdomain.Order, setting settingsintegration.AffiliateSetting) (decimal.Decimal, map[string]string, map[string]string, error) {
	amounts, totals := map[string]string{}, map[string]string{}
	if order == nil || s.productRepo == nil {
		return decimal.Zero, amounts, totals, nil
	}
	products, err := s.productRepo.ListByIDs(collectAffiliateProductIDs(order))
	if err != nil {
		return decimal.Zero, nil, nil, err
	}
	listed := map[uint]bool{}
	for _, id := range setting.EligibleProductIDs {
		listed[id] = true
	}
	eligible := map[uint]bool{}
	for _, p := range products {
		slug := strings.ToLower(p.Slug)
		eligible[p.ID] = listed[p.ID] && (strings.HasPrefix(slug, "gpt-") || strings.HasPrefix(slug, "chatgpt-"))
	}
	orders := order.Children
	if len(orders) == 0 {
		orders = []orderdomain.Order{*order}
	}
	total := decimal.Zero
	for _, current := range orders {
		net := decimal.Zero
		for _, item := range current.Items {
			if eligible[item.ProductID] {
				value := item.TotalPrice.Decimal.Sub(item.CouponDiscount.Decimal)
				if value.IsPositive() {
					net = net.Add(value)
				}
			}
		}
		if net.GreaterThan(current.TotalAmount.Decimal) {
			net = current.TotalAmount.Decimal
		}
		if net.IsNegative() {
			net = decimal.Zero
		}
		key := strconv.FormatUint(uint64(current.ID), 10)
		amounts[key] = net.Round(2).StringFixed(2)
		totals[key] = current.TotalAmount.Decimal.Round(2).StringFixed(2)
		total = total.Add(net).Round(2)
	}
	if total.GreaterThan(order.TotalAmount.Decimal) && total.IsPositive() {
		factor := order.TotalAmount.Decimal.Div(total)
		total = decimal.Zero
		for key, value := range amounts {
			scaled := decimal.RequireFromString(value).Mul(factor).Round(2)
			amounts[key] = scaled.StringFixed(2)
			total = total.Add(scaled)
		}
	}
	key := strconv.FormatUint(uint64(order.ID), 10)
	amounts[key] = total.StringFixed(2)
	totals[key] = order.TotalAmount.Decimal.Round(2).StringFixed(2)
	return total, amounts, totals, nil
}

func collectAffiliateProductIDs(order *orderdomain.Order) []uint {
	if order == nil {
		return nil
	}
	ids := make([]uint, 0)
	seen := make(map[uint]struct{})
	appendItem := func(item orderdomain.OrderItem) {
		if item.ProductID == 0 {
			return
		}
		if _, ok := seen[item.ProductID]; ok {
			return
		}
		seen[item.ProductID] = struct{}{}
		ids = append(ids, item.ProductID)
	}
	for _, item := range order.Items {
		appendItem(item)
	}
	for _, child := range order.Children {
		for _, item := range child.Items {
			appendItem(item)
		}
	}
	return ids
}

func buildSplitCommissionType(sourceID uint) string {
	suffix := strconv.FormatInt(time.Now().UnixNano()%1000000, 10)
	base := affiliateSplitTypePrefix + strconv.FormatUint(uint64(sourceID), 36)
	result := base + suffix
	if len(result) > 20 {
		return result[:20]
	}
	return result
}
