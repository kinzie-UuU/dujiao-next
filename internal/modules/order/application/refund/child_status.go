package refund

import (
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

// applyParentRefundChildStatusUpdates 根据父订单退款结果统一更新子订单状态。
// 规则:子订单退款状态始终跟随父订单,避免分支逻辑造成状态混乱。
//
// orderStore 必须来自订单工作单元，确保父子状态在同一事务中更新。
func applyParentRefundChildStatusUpdates(orderStore ordercontract.Store, parentOrderID uint, parentTargetStatus string, now time.Time) error {
	if orderStore == nil || parentOrderID == 0 {
		return nil
	}

	target := strings.ToLower(strings.TrimSpace(parentTargetStatus))
	if target != constants.OrderStatusPartiallyRefunded && target != constants.OrderStatusRefunded {
		return nil
	}

	affected, err := orderStore.UpdateChildrenStatus(parentOrderID, target, now)
	if err != nil {
		return err
	}
	if affected > 0 {
		logger.Debugw("refund_child_status_propagated",
			"parent_order_id", parentOrderID,
			"target_status", target,
			"rows_affected", affected,
		)
	}
	return nil
}

// lockRefundOrder always locks the parent before a child, matching parent refund
// status propagation and serializing the shared refundable budget.
func lockRefundOrder(store ordercontract.Store, id uint) (*orderdomain.Order, error) {
	snapshot, err := store.GetByID(id)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, ErrOrderNotFound
	}
	rootID := id
	if snapshot.ParentID != nil && *snapshot.ParentID != 0 {
		rootID = *snapshot.ParentID
	}
	parent, err := store.GetByIDForUpdate(rootID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, ErrOrderNotFound
	}
	// Older releases recorded child refunds without adding them to the parent.
	// Sum actual refund records (not parent+child counters, which double-count
	// new refunds) and never reduce an existing total.
	children, err := store.ListChildren(rootID)
	if err != nil {
		return nil, err
	}
	if len(children) > 0 {
		ids := []uint{rootID}
		childTotal := decimal.Zero
		for _, child := range children {
			ids = append(ids, child.ID)
			childTotal = childTotal.Add(child.RefundedAmount.Decimal)
		}
		records, err := store.ListRefundRecordsByOrderIDs(ids)
		if err != nil {
			return nil, err
		}
		recorded := decimal.Zero
		for _, record := range records {
			recorded = recorded.Add(record.Amount.Decimal)
		}
		total := decimal.Max(parent.RefundedAmount.Decimal, recorded, childTotal).Round(2)
		if total.GreaterThan(parent.RefundedAmount.Decimal) {
			if err := store.UpdateFields(rootID, map[string]interface{}{"refunded_amount": money.FromDecimal(total)}); err != nil {
				return nil, err
			}
			parent.RefundedAmount = money.FromDecimal(total)
		}
	}
	if rootID == id {
		return parent, nil
	}
	return store.GetByIDForUpdate(id)
}

// reserveParentRefund accounts for a child refund in the parent budget in the
// same transaction as the wallet credit or manual refund record.
func reserveParentRefund(store ordercontract.Store, order *orderdomain.Order, amount decimal.Decimal, now time.Time) error {
	if order.ParentID == nil || *order.ParentID == 0 {
		return nil
	}
	parent, err := store.GetByIDForUpdate(*order.ParentID)
	if err != nil {
		return err
	}
	if parent == nil {
		return ErrOrderNotFound
	}
	refunded := parent.RefundedAmount.Decimal.Round(2)
	if amount.GreaterThan(parent.TotalAmount.Decimal.Sub(refunded).Round(2)) {
		return walletcontract.ErrRefundExceeded
	}
	return store.UpdateFields(parent.ID, map[string]interface{}{"refunded_amount": money.FromDecimal(refunded.Add(amount).Round(2)), "updated_at": now})
}
