package application

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	procurementcontract "github.com/dujiao-next/internal/modules/procurement/contract"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"

	"github.com/google/uuid"
)

// CreateForOrder 为已支付订单创建采购单（上游交付类型）
func (s *Service) CreateForOrder(orderID uint) error {
	order, err := s.orderRepo.GetByID(orderID)
	if err != nil {
		return fmt.Errorf("load order: %w", err)
	}
	if order == nil {
		return procurementcontract.ErrOrderNotFound
	}

	// 父订单有子订单：遍历子订单
	if order.ParentID == nil && len(order.Children) > 0 {
		var childErrors []error
		for i := range order.Children {
			child := &order.Children[i]
			if !s.hasUpstreamItems(child) {
				continue
			}
			if err := s.createProcurementForSingleOrder(child); err != nil {
				logger.Warnw("procurement_create_child_failed",
					"parent_order_id", orderID,
					"child_order_id", child.ID,
					"error", err,
				)
				if !errors.Is(err, procurementcontract.ErrExists) {
					childErrors = append(childErrors, err)
				}
			}
		}
		return errors.Join(childErrors...)
	}

	// 单订单
	if !s.hasUpstreamItems(order) {
		return nil
	}
	return s.createProcurementForSingleOrder(order)
}

// createProcurementForSingleOrder 为单个订单创建采购单
func (s *Service) createProcurementForSingleOrder(order *procurementdomain.LocalOrder) error {
	// 检查是否已存在
	existing, err := s.procRepo.GetByLocalOrderID(order.ID)
	if err != nil {
		return fmt.Errorf("check existing procurement: %w", err)
	}
	if existing != nil {
		return procurementcontract.ErrExists
	}

	if len(order.Items) == 0 {
		return fmt.Errorf("order %d has no items", order.ID)
	}
	item := order.Items[0]

	// 查找商品映射
	connectionID, found, err := s.mappingRepo.FindConnectionID(item.ProductID)
	if err != nil {
		return fmt.Errorf("lookup product mapping: %w", err)
	}
	if !found {
		return fmt.Errorf("no product mapping for product %d", item.ProductID)
	}

	procOrder := &procurementdomain.Order{
		ConnectionID:    connectionID,
		LocalOrderID:    order.ID,
		LocalOrderNo:    order.OrderNo,
		Status:          "pending",
		LocalSellAmount: order.TotalAmount,
		Currency:        order.Currency,
		TraceID:         uuid.NewString(),
	}

	if err := s.procRepo.Create(procOrder); err != nil {
		return fmt.Errorf("create procurement order: %w", err)
	}

	logger.Infow("procurement_order_created",
		"procurement_order_id", procOrder.ID,
		"local_order_id", order.ID,
		"connection_id", connectionID,
	)

	return s.enqueuePending(procOrder)
}

// A failed enqueue must leave an order the operator can retry. If a worker
// already advanced the order, keep its state instead.
func (s *Service) enqueuePending(order *procurementdomain.Order) error {
	if s.queue == nil {
		return nil
	}
	err := s.queue.EnqueueSubmit(order.ID)
	if err == nil {
		return nil
	}
	message := fmt.Sprintf("enqueue procurement submission: %v", err)
	failed, updateErr := s.procRepo.UpdateStatus(order.ID, constants.ProcurementStatusPending, constants.ProcurementStatusFailed, map[string]interface{}{
		"error_message": message,
		"updated_at":    time.Now(),
	})
	if updateErr != nil {
		return errors.Join(err, updateErr)
	}
	if failed {
		s.notifyProcurementFailure(order, message)
		return err
	}
	return nil
}

// hasUpstreamItems 检查订单是否包含上游交付类型的商品
func (s *Service) hasUpstreamItems(order *procurementdomain.LocalOrder) bool {
	for _, item := range order.Items {
		if strings.TrimSpace(item.FulfillmentType) == constants.FulfillmentTypeUpstream {
			return true
		}
	}
	return false
}
