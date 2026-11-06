package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	procurementcontract "github.com/dujiao-next/internal/modules/procurement/contract"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
)

// SubmitToUpstream Worker 调用：向上游站点提交采购单
func (s *Service) SubmitToUpstream(procurementOrderID uint) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return procurementcontract.ErrNotFound
	}

	// 校验状态
	if procOrder.Status != "pending" && procOrder.Status != "failed" {
		return procurementcontract.ErrStatusInvalid
	}

	// Legacy rows may already have reached a supplier. Do not infer "never sent"
	// from a missing receipt or retry counter after upgrading.
	if procOrder.SubmissionVersion != 1 || procOrder.SubmittedAt != nil || procOrder.SubmissionRequestSHA256 != "" {
		return s.markSubmissionUncertain(procOrder, "Legacy or previously attempted purchase requires reconciliation")
	}

	// 获取连接和适配器
	connection, err := s.connections.Open(procOrder.ConnectionID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("load connection failed: %v", err))
		return fmt.Errorf("load connection: %w", err)
	}
	if connection == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("connection %d not found", procOrder.ConnectionID))
		return nil // 永久性错误，不重试
	}

	// 加载本地订单获取 SKU 信息
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("load local order failed: %v", err))
		return fmt.Errorf("load local order: %w", err)
	}
	if localOrder == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d not found", procOrder.LocalOrderID))
		return nil // 永久性错误，不重试
	}
	if localOrder.Status != constants.OrderStatusPaid && localOrder.Status != constants.OrderStatusFulfilling {
		s.rejectProcurement(procOrder, "local order is no longer awaiting fulfillment")
		return nil
	}
	if len(localOrder.Items) == 0 {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d has no items", localOrder.ID))
		return nil // 永久性错误，不重试
	}
	item := localOrder.Items[0]

	// 查找 SKU 映射
	upstreamSKUID, found, err := s.skuMapRepo.FindUpstreamSKUID(item.SKUID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("lookup sku mapping failed: %v", err))
		return fmt.Errorf("lookup sku mapping: %w", err)
	}
	if !found {
		s.rejectProcurement(procOrder, fmt.Sprintf("no sku mapping for local sku %d", item.SKUID))
		return nil // 永久性错误，不重试
	}

	// 构建上游请求
	req := procurementcontract.CreateOrderRequest{
		ProcurementCreatedAt: procOrder.CreatedAt,
		SKUID:                upstreamSKUID,
		Quantity:             item.Quantity,
		DownstreamOrderNo:    localOrder.OrderNo,
		TraceID:              procOrder.TraceID,
		CallbackURL:          connection.CallbackURL(),
	}

	// 传递人工表单数据（如有）
	if len(item.ManualFormSubmissionJSON) > 0 {
		req.ManualFormData = item.ManualFormSubmissionJSON
	}

	requestBytes, err := json.Marshal(struct {
		ConnectionID uint
		Request      procurementcontract.CreateOrderRequest
	}{procOrder.ConnectionID, req})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(requestBytes)
	claimed, err := s.procRepo.BeginSubmission(procOrder.ID, procOrder.Status, hex.EncodeToString(sum[:]), connection.IdentitySHA256(), time.Now())
	if err != nil {
		return err
	}
	if !claimed {
		return procurementcontract.ErrStatusInvalid
	}
	procOrder.Status = constants.ProcurementStatusSubmitted

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := connection.CreateOrder(ctx, req)
	if err != nil {
		return s.markSubmissionUncertain(procOrder, "Supplier response unavailable; reconcile before retry, cancellation or refund")
	}

	if resp == nil || !resp.OK || (resp.OrderID == 0 && strings.TrimSpace(resp.OrderNo) == "") {
		return s.markSubmissionUncertain(procOrder, "Supplier did not return a conclusive receipt; reconcile before retry, cancellation or refund")
	}

	// 成功：更新状态，重置 retry_count 用于轮询阶段
	now := time.Now()
	updates := map[string]interface{}{
		"upstream_order_id": resp.OrderID,
		"upstream_order_no": resp.OrderNo,
		"upstream_amount":   resp.Amount,
		"upstream_currency": resp.Currency,
		"error_message":     "",
		"retry_count":       0,
		"updated_at":        now,
	}
	accepted, err := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, constants.ProcurementStatusAccepted, updates)
	if err != nil {
		return fmt.Errorf("update procurement status: %w", err)
	}
	if !accepted {
		// 上游回调先于下单响应到达（如秒发卡密），采购单已被推进，不能再回退为 accepted
		logger.Infow("procurement_accept_skipped_status_changed", "procurement_order_id", procOrder.ID)
		return nil
	}

	logger.Infow("procurement_order_accepted",
		"procurement_order_id", procOrder.ID,
		"upstream_order_id", resp.OrderID,
		"upstream_order_no", resp.OrderNo,
	)

	// 更新本地订单状态为 fulfilling。
	// 只推进已支付订单，不能覆盖并发完成的交付或退款。
	_, err = s.orderRepo.UpdateStatusIfCurrent(localOrder.ID, []string{constants.OrderStatusPaid}, constants.OrderStatusFulfilling, map[string]interface{}{
		"updated_at": now,
	})

	if err != nil {
		return fmt.Errorf("update local procurement status: %w", err)
	}

	// 入队轮询任务（30s 延迟，作为回调的 fallback）
	if s.queue != nil {
		_ = s.queue.EnqueuePoll(procOrder.ID, 30*time.Second)
	}

	return nil
}

// markProcurementError 记录错误信息但不改变状态（用于瞬态错误，asynq 可重试）
func (s *Service) markProcurementError(procOrder *procurementdomain.Order, errMsg string) {
	now := time.Now()
	_, _ = s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, procOrder.Status, map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	})
	logger.Warnw("procurement_prepare_error",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)
}

// rejectProcurement 将采购单标记为 rejected（用于永久性配置错误，不值得重试）
// 同时回退本地订单状态并通知管理员
func (s *Service) rejectProcurement(procOrder *procurementdomain.Order, errMsg string) {
	now := time.Now()
	rejected, _ := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, constants.ProcurementStatusRejected, map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	})
	if !rejected {
		return
	}
	logger.Warnw("procurement_rejected_config_error",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)
	s.rollbackLocalOrderOnProcurementFailure(procOrder, errMsg)
}

// rollbackLocalOrderOnProcurementFailure 采购单终态失败时回退本地订单状态并通知管理员
func (s *Service) rollbackLocalOrderOnProcurementFailure(procOrder *procurementdomain.Order, errMsg string) {
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil || localOrder == nil {
		return
	}
	if localOrder.Status == constants.OrderStatusFulfilling {
		now := time.Now()
		rolledBack, updateErr := s.orderRepo.UpdateStatusIfCurrent(localOrder.ID, []string{constants.OrderStatusFulfilling}, constants.OrderStatusPaid, map[string]interface{}{
			"updated_at": now,
		})
		if updateErr != nil || !rolledBack {
			return
		}
		// 如果是子订单，同步父订单状态
		if localOrder.ParentID != nil && s.orderLifecycle != nil {
			_, _ = s.orderLifecycle.SyncParentStatus(*localOrder.ParentID, now)
		}
		logger.Infow("procurement_failure_order_rolled_back",
			"procurement_order_id", procOrder.ID,
			"local_order_id", localOrder.ID,
			"from_status", constants.OrderStatusFulfilling,
			"to_status", constants.OrderStatusPaid,
		)
	}
	s.notifyProcurementFailure(procOrder, errMsg)
}

// notifyProcurementFailure 发送采购失败异常告警
func (s *Service) notifyProcurementFailure(procOrder *procurementdomain.Order, errMsg string) {
	if s.notifications == nil {
		return
	}
	_ = s.notifications.NotifyFailure(procOrder, errMsg)
}

// Uncertain is not a failed purchase: no enqueue, automatic rejection, local
// status rollback, or payment refund is justified by a missing response.
func (s *Service) markSubmissionUncertain(procOrder *procurementdomain.Order, message string) error {
	changed, err := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, constants.ProcurementStatusUncertain, map[string]interface{}{"error_message": message, "next_retry_at": nil, "updated_at": time.Now()})
	if err != nil {
		return fmt.Errorf("persist uncertain procurement: %w", err)
	}
	if changed {
		s.notifyProcurementFailure(procOrder, message)
	}
	return nil
}
