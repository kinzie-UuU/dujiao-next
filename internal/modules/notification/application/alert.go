package application

import (
	"context"
	"fmt"
	"time"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/constants"

	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/modules/notification/application/format"
	settingsmessaging "github.com/dujiao-next/internal/modules/settings/schema/messaging"
	"github.com/dujiao-next/internal/queue"
)

func (s *Service) dispatchExceptionAlertCheck(ctx context.Context, setting settingsmessaging.NotificationCenterSetting, payload queue.NotificationDispatchPayload) error {
	if s.dashboardSvc == nil || s.settingService == nil {
		return nil
	}

	dashboardSetting, err := s.settingService.GetDashboardSetting()
	if err != nil {
		return err
	}

	var firstErr error
	if setting.InventoryAlertEnabled {
		inventoryAlerts, err := s.dashboardSvc.GetInventoryAlertItems(ctx, dashboardSetting.Alert.LowStockThreshold)
		if err != nil {
			return err
		}
		for _, itemPayload := range format.BuildInventoryAlertDispatchPayloads(setting, dashboardSetting, payload, inventoryAlerts) {
			allowed, intervalErr := acquireInventoryAlertInterval(ctx, setting.InventoryAlertIntervalSeconds, itemPayload)
			if intervalErr != nil {
				logger.Warnw("notification_inventory_alert_interval_failed", "error", intervalErr)
			}
			if intervalErr == nil && !allowed {
				continue
			}
			if err := s.dispatchSingleEvent(ctx, setting, itemPayload); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	paymentOrderAlertNow := time.Now()
	paymentOrderAlertStart := paymentOrderAlertNow.Add(-time.Duration(setting.PaymentOrderAlertCheckSeconds) * time.Second)
	paymentOrderCounts, err := s.dashboardSvc.GetPaymentOrderAlertCounts(ctx, paymentOrderAlertStart, paymentOrderAlertNow)
	if err != nil {
		return err
	}

	for _, itemPayload := range format.BuildPaymentOrderAlertDispatchPayloads(setting, dashboardSetting, payload, paymentOrderCounts) {
		if resolvePaymentOrderAlertTypeKey(itemPayload.Data) == constants.NotificationAlertTypePaymentsFailed {
			if err := s.dispatchPaymentFailureAlert(ctx, setting, itemPayload, paymentOrderCounts.FailedPaymentIDs); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		allowed, intervalErr := acquirePaymentOrderAlertInterval(ctx, setting.PaymentOrderAlertIntervalSeconds, itemPayload)
		if intervalErr != nil {
			logger.Warnw("notification_payment_order_alert_interval_failed", "error", intervalErr)
		}
		if intervalErr == nil && !allowed {
			continue
		}
		if err := s.dispatchSingleEvent(ctx, setting, itemPayload); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// A shrinking or unchanged failure set is not a new incident. Remember attempts
// only after a successful send; retain the normal interval for new failures.
func (s *Service) dispatchPaymentFailureAlert(ctx context.Context, setting settingsmessaging.NotificationCenterSetting, payload queue.NotificationDispatchPayload, ids []uint) error {
	if !(setting.Channels.Email.Enabled && len(setting.Channels.Email.Recipients) > 0) &&
		!(setting.Channels.Telegram.Enabled && len(setting.Channels.Telegram.Recipients) > 0) {
		return nil
	}
	const lockKey = "notification:payment_order_interval:payments_failed"
	const seenKey = "notification:payment_failures:seen"
	interval := settingsmessaging.NormalizeNotificationPaymentOrderAlertInterval(setting.PaymentOrderAlertIntervalSeconds)
	token := fmt.Sprintf("%d", time.Now().UnixNano())
	allowed, err := cache.SetNX(ctx, lockKey, token, time.Duration(interval)*time.Second)
	if err != nil || !allowed {
		return err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = cache.DelIfValue(cleanupCtx, lockKey, token)
		}
	}()
	var seen []uint
	if _, err := cache.GetJSON(ctx, seenKey, &seen); err != nil {
		return err
	}
	if !hasNewPaymentFailure(ids, seen) {
		return nil
	}
	// This path owns retry and interval state; the generic content dedupe must
	// not swallow a retry after a failed send (or a new failure with equal count).
	payload.Force = true
	if err := s.dispatchSingleEvent(ctx, setting, payload); err != nil {
		return err
	}
	window := settingsmessaging.NormalizeNotificationPaymentOrderAlertCheck(setting.PaymentOrderAlertCheckSeconds)
	if err := cache.SetJSON(ctx, seenKey, ids, time.Duration(window+interval)*time.Second); err != nil {
		return err
	}
	keepLock = true
	return nil
}

func hasNewPaymentFailure(ids, seen []uint) bool {
	known := make(map[uint]struct{}, len(seen))
	for _, id := range seen {
		known[id] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return true
		}
	}
	return false
}
