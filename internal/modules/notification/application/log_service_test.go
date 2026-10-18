package application

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"

	"github.com/dujiao-next/internal/constants"
	dashboard "github.com/dujiao-next/internal/modules/dashboard/contract"
	"github.com/dujiao-next/internal/modules/notification/application/format"
	"github.com/dujiao-next/internal/modules/notification/contract"
	"github.com/dujiao-next/internal/modules/notification/domain"
	settingsmessaging "github.com/dujiao-next/internal/modules/settings/schema/messaging"
	settingsstorefront "github.com/dujiao-next/internal/modules/settings/schema/storefront"
	"github.com/dujiao-next/internal/queue"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

type notificationSettingsStub struct {
	notification settingsmessaging.NotificationCenterSetting
	dashboard    settingsstorefront.DashboardSetting
}

func (s notificationSettingsStub) GetNotificationCenterSetting() (settingsmessaging.NotificationCenterSetting, error) {
	return s.notification, nil
}

func (s notificationSettingsStub) GetDashboardSetting() (settingsstorefront.DashboardSetting, error) {
	return s.dashboard, nil
}

type notificationEmailStub struct{}

func (notificationEmailStub) SendCustomEmail(recipient, _, _ string) error {
	if strings.Contains(recipient, "failure") {
		return errors.New("simulated email failure")
	}
	return nil
}

type notificationLogRepositoryStub struct {
	items []domain.NotificationLog
}

func (r *notificationLogRepositoryStub) Create(item *domain.NotificationLog) error {
	if item != nil {
		copy := *item
		copy.ID = uint(len(r.items) + 1)
		r.items = append(r.items, copy)
	}
	return nil
}

func (r *notificationLogRepositoryStub) ListAdmin(filter contract.LogListFilter) ([]domain.NotificationLog, int64, error) {
	result := make([]domain.NotificationLog, 0, len(r.items))
	for _, item := range r.items {
		if filter.EventType != "" && item.EventType != filter.EventType {
			continue
		}
		if filter.IsTest != nil && item.IsTest != *filter.IsTest {
			continue
		}
		result = append(result, item)
	}
	return result, int64(len(result)), nil
}

func setupLogService(t *testing.T) (*Service, *LogService) {
	t.Helper()
	logService := NewLogService(&notificationLogRepositoryStub{})
	setting := settingsmessaging.NotificationCenterSetting{
		DefaultLocale: constants.LocaleEnUS,
		Scenes: settingsmessaging.NotificationSceneSetting{
			OrderPaidSuccess: true,
			ExceptionAlert:   true,
		},
		Channels: settingsmessaging.NotificationChannelsSetting{
			Email: settingsmessaging.NotificationChannelSetting{
				Enabled:    true,
				Recipients: []string{"success@example.com", "failure@example.com"},
			},
		},
		Templates: settingsmessaging.NotificationTemplatesSetting{
			OrderPaidSuccess: settingsmessaging.NotificationSceneTemplate{
				ENUS: settingsmessaging.NotificationLocalizedTemplate{
					Title: "Order {{order_no}}",
					Body:  "Customer {{customer_email}}",
				},
			},
			ExceptionAlert: settingsmessaging.NotificationSceneTemplate{
				ENUS: settingsmessaging.NotificationLocalizedTemplate{
					Title: "Alert {{message}}",
					Body:  "{{message}}",
				},
			},
		},
	}
	service := NewService(notificationSettingsStub{notification: setting}, notificationEmailStub{}, nil, nil, logService, nil)
	return service, logService
}

func TestServiceSendTestRecordsSuccessLog(t *testing.T) {
	service, logService := setupLogService(t)
	if err := service.SendTest(context.Background(), contract.TestSendInput{
		Channel: "email",
		Target:  "success@example.com",
		Scene:   constants.NotificationEventOrderPaidSuccess,
		Locale:  constants.LocaleEnUS,
	}); err != nil {
		t.Fatalf("SendTest failed: %v", err)
	}

	isTest := true
	items, total, err := logService.ListForAdmin(contract.LogListFilter{
		Page: 1, PageSize: 10, EventType: constants.NotificationEventOrderPaidSuccess, IsTest: &isTest,
	})
	if err != nil {
		t.Fatalf("list notification logs failed: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 test log, total=%d len=%d", total, len(items))
	}
	if items[0].Status != notificationLogStatusSuccess || !items[0].IsTest {
		t.Fatalf("unexpected log: %#v", items[0])
	}
	if !strings.Contains(items[0].Title, "DJ202603230001") {
		t.Fatalf("title should include sample order no, got %s", items[0].Title)
	}
}

func TestServiceDispatchSingleEventRecordsPerRecipientResult(t *testing.T) {
	service, logService := setupLogService(t)
	setting, err := service.settingService.GetNotificationCenterSetting()
	if err != nil {
		t.Fatalf("get notification center setting failed: %v", err)
	}
	dispatchErr := service.dispatchSingleEvent(context.Background(), setting, queue.NotificationDispatchPayload{
		EventType: constants.NotificationEventOrderPaidSuccess,
		BizType:   constants.NotificationBizTypeOrder,
		BizID:     88,
		Locale:    constants.LocaleEnUS,
		Force:     true,
		Data: map[string]interface{}{
			"order_no":       "DJ-LOG-88",
			"customer_email": "member@example.com",
		},
	})
	if !errors.Is(dispatchErr, contract.ErrSendFailed) {
		t.Fatalf("expected send failure, got %v", dispatchErr)
	}

	items, total, err := logService.ListForAdmin(contract.LogListFilter{Page: 1, PageSize: 10, EventType: constants.NotificationEventOrderPaidSuccess})
	if err != nil {
		t.Fatalf("list notification logs failed: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 recipient logs, total=%d len=%d", total, len(items))
	}
	statuses := map[string]string{}
	for _, item := range items {
		statuses[item.Recipient] = item.Status
	}
	if statuses["success@example.com"] != notificationLogStatusSuccess {
		t.Fatalf("success recipient status mismatch: %v", statuses)
	}
	if statuses["failure@example.com"] != notificationLogStatusFailed {
		t.Fatalf("failure recipient status mismatch: %v", statuses)
	}
}

type loginTelegramSpy struct {
	messages []string
	err      error
	cancel   context.CancelFunc
}

func (s *loginTelegramSpy) SendMessage(_ context.Context, target, message string) error {
	s.messages = append(s.messages, target+":"+message)
	if s.cancel != nil {
		s.cancel()
	}
	return s.err
}

type loginEmailSpy struct{ calls int }

func (s *loginEmailSpy) SendCustomEmail(_, _, _ string) error { s.calls++; return nil }

func TestLoginNotificationUsesExceptionSwitchFixedTemplateAndTelegramOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			setting := settingsmessaging.NotificationCenterDefaultSetting()
			setting.Scenes.ExceptionAlert = enabled
			setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123456789"}}
			setting.Channels.Email = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"other@example.com"}}
			setting.Templates.ExceptionAlert.ZHCN = settingsmessaging.NotificationLocalizedTemplate{Title: "business", Body: "editable template"}
			tg := &loginTelegramSpy{}
			email := &loginEmailSpy{}
			svc := NewService(notificationSettingsStub{notification: setting}, email, nil, nil, nil, tg)
			payload := queue.NotificationDispatchPayload{EventType: constants.NotificationEventAdminLoginSuccess, BizType: "admin_login", BizID: 7, Data: map[string]interface{}{"admin_username": "admin", "login_at": "2026-10-16T12:00:00Z", "client_ip": "203.0.113.7", "login_method": "password", "login_event_id": "unique"}}
			if err := svc.Dispatch(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			want := 0
			if enabled {
				want = 1
			}
			if len(tg.messages) != want || email.calls != 0 {
				t.Fatalf("telegram=%d email=%d", len(tg.messages), email.calls)
			}
			if enabled && (!strings.Contains(tg.messages[0], "管理员登录成功") || strings.Contains(tg.messages[0], "editable template") || !strings.Contains(tg.messages[0], "203.0.113.7") || !strings.Contains(tg.messages[0], "2026-10-16T12:00:00Z")) {
				t.Fatal(tg.messages)
			}
			// Configuration round trips through the existing UI schema retain its controlling switch.
			if setting.Scenes.IsSceneEnabled(constants.NotificationEventAdminLoginSuccess) != enabled {
				t.Fatal("scene switch mismatch")
			}
		})
	}
}
func TestLoginEventDedupeSeparatesLoginsButKeepsRetryIdentity(t *testing.T) {
	p := queue.NotificationDispatchPayload{EventType: constants.NotificationEventAdminLoginSuccess, BizID: 7, Data: map[string]interface{}{"login_event_id": "one"}}
	first := buildNotificationDedupeKey(p)
	if first != buildNotificationDedupeKey(p) {
		t.Fatal("unstable retry identity")
	}
	p.Data["login_event_id"] = "two"
	if first == buildNotificationDedupeKey(p) {
		t.Fatal("second login suppressed")
	}
}
func TestLoginTelegramFailureIsRetryable(t *testing.T) {
	for _, event := range []string{constants.NotificationEventAdminLoginSuccess, constants.NotificationEventAdminLoginFailed, constants.NotificationEventAdminPaymentConfigChanged} {
		t.Run(event, func(t *testing.T) { testSecurityTelegramFailureIsRetryable(t, event) })
	}
}
func testSecurityTelegramFailureIsRetryable(t *testing.T, event string) {
	redis := miniredis.RunT(t)
	port, err := strconv.Atoi(redis.Port())
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.InitRedis(&config.RedisConfig{Enabled: true, Host: redis.Host(), Port: port, Prefix: "login-test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setting := settingsmessaging.NotificationCenterDefaultSetting()
	setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123456789"}}
	tg := &loginTelegramSpy{err: errors.New("temporary"), cancel: cancel}
	svc := NewService(notificationSettingsStub{notification: setting}, nil, nil, nil, nil, tg)
	p := queue.NotificationDispatchPayload{EventType: event, Data: map[string]interface{}{"login_event_id": "retry"}}
	if svc.Dispatch(ctx, p) == nil {
		t.Fatal("failure swallowed")
	}
	if len(redis.Keys()) != 0 {
		t.Fatalf("failed send left dedupe lock after cancellation: %v", redis.Keys())
	}
	tg.err = nil
	tg.cancel = nil
	if err := svc.Dispatch(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 2 {
		t.Fatal("retry was skipped")
	}
	if len(redis.Keys()) != 1 {
		t.Fatal("successful send did not preserve dedupe")
	}
	if err := svc.Dispatch(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 2 {
		t.Fatal("successful retry sent twice")
	}
}

func TestNewSecurityEventsUseFixedTelegramTemplatesAndExceptionSwitch(t *testing.T) {
	for _, event := range []string{constants.NotificationEventAdminLoginFailed, constants.NotificationEventAdminPaymentConfigChanged} {
		for _, enabled := range []bool{false, true} {
			setting := settingsmessaging.NotificationCenterDefaultSetting()
			setting.Scenes.ExceptionAlert = enabled
			setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123"}}
			setting.Channels.Email = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"other@example.com"}}
			setting.Templates.ExceptionAlert.ZHCN = settingsmessaging.NotificationLocalizedTemplate{Title: "editable", Body: "secret {{password}}"}
			tg := &loginTelegramSpy{}
			email := &loginEmailSpy{}
			svc := NewService(notificationSettingsStub{notification: setting}, email, nil, nil, nil, tg)
			err := svc.Dispatch(context.Background(), queue.NotificationDispatchPayload{EventType: event, Data: map[string]interface{}{"password": "private-secret", "action": "update", "changed_fields": "config_json", "object_id": "7"}})
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if enabled {
				want = 1
			}
			if len(tg.messages) != want || email.calls != 0 {
				t.Fatal("channel or scene mismatch")
			}
			if enabled && (strings.Contains(tg.messages[0], "private-secret") || strings.Contains(tg.messages[0], "editable")) {
				t.Fatal("editable template or secret leaked")
			}
			if enabled && event == constants.NotificationEventAdminPaymentConfigChanged && !strings.Contains(tg.messages[0], "config_json") {
				t.Fatal("change field missing")
			}
		}
	}
}

func TestInventoryAlertSwitchCompatibilityAndScopedPatch(t *testing.T) {
	original := settingsmessaging.NotificationCenterDefaultSetting()
	legacy := settingsmessaging.DecodeNotificationCenterSetting(jsonmap.JSON{"dedupe_ttl_seconds": 300}, original)
	if !legacy.InventoryAlertEnabled {
		t.Fatal("legacy settings must retain enabled inventory alerts")
	}
	disabled := false
	updated, err := settingsmessaging.ApplyNotificationCenterSettingPatch(original, settingsmessaging.NotificationCenterSettingPatch{InventoryAlertEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.InventoryAlertEnabled {
		t.Fatal("false patch was lost")
	}
	expected := settingsmessaging.NormalizeNotificationCenterSetting(original)
	expected.InventoryAlertEnabled = false
	if !reflect.DeepEqual(expected, updated) {
		t.Fatal("inventory patch changed unrelated settings")
	}
	raw := settingsmessaging.NotificationCenterSettingToMap(updated)
	restored := settingsmessaging.DecodeNotificationCenterSetting(raw, original)
	if restored.InventoryAlertEnabled {
		t.Fatal("stored false was lost")
	}
	restored, err = settingsmessaging.ApplyNotificationCenterSettingPatch(restored, settingsmessaging.NotificationCenterSettingPatch{})
	if err != nil || restored.InventoryAlertEnabled {
		t.Fatalf("omitted patch must preserve false: %v", err)
	}
}

func TestInventorySwitchDropsQueuedAlertsOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, tc := range []struct {
			name, event, key string
			inventory        bool
		}{
			{"low", constants.NotificationEventExceptionAlert, constants.NotificationAlertTypeLowStockProducts, true},
			{"sold-out", constants.NotificationEventExceptionAlert, constants.NotificationAlertTypeOutOfStockProducts, true},
			{"legacy-localized", constants.NotificationEventExceptionAlert, "低库存商品", true},
			{"payment-failed", constants.NotificationEventExceptionAlert, constants.NotificationAlertTypePaymentsFailed, false},
			{"pending-payment", constants.NotificationEventExceptionAlert, constants.NotificationAlertTypePendingOrders, false},
			{"generic-exception", constants.NotificationEventExceptionAlert, "other", false},
			{"login", constants.NotificationEventAdminLoginSuccess, "", false},
			{"login-failed", constants.NotificationEventAdminLoginFailed, "", false},
			{"payment-config", constants.NotificationEventAdminPaymentConfigChanged, "", false},
			{"order", constants.NotificationEventOrderPaidSuccess, "", false},
			{"recharge", constants.NotificationEventWalletRechargeSuccess, "", false},
			{"manual", constants.NotificationEventManualFulfillmentPending, "", false},
		} {
			t.Run(tc.name+strconv.FormatBool(enabled), func(t *testing.T) {
				setting := settingsmessaging.NotificationCenterDefaultSetting()
				setting.InventoryAlertEnabled = enabled
				setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123"}}
				tg := &loginTelegramSpy{}
				svc := NewService(notificationSettingsStub{notification: setting}, nil, nil, nil, nil, tg)
				payload := queue.NotificationDispatchPayload{EventType: tc.event, Force: true, Data: map[string]interface{}{"alert_type": tc.key}}
				if err := svc.Dispatch(context.Background(), payload); err != nil {
					t.Fatal(err)
				}
				want := 1
				if tc.inventory && !enabled {
					want = 0
				}
				if len(tg.messages) != want {
					t.Fatalf("messages=%d want=%d", len(tg.messages), want)
				}
			})
		}
	}
}

type inventorySwitchDashboardSpy struct{ inventoryCalls, paymentCalls int }

func (s *inventorySwitchDashboardSpy) LoadDashboardAlertSetting() settingsstorefront.DashboardAlertSetting {
	return settingsstorefront.DashboardAlertSetting{}
}
func (s *inventorySwitchDashboardSpy) GetInventoryAlertItems(context.Context, int64) ([]dashboard.InventoryAlertRow, error) {
	s.inventoryCalls++
	return nil, errors.New("inventory unavailable")
}
func (s *inventorySwitchDashboardSpy) GetPaymentOrderAlertCounts(context.Context, time.Time, time.Time) (dashboard.PaymentOrderAlertCountsRow, error) {
	s.paymentCalls++
	return dashboard.PaymentOrderAlertCountsRow{PendingPaymentOrders: 2, PaymentsFailed: 2}, nil
}
func TestDisabledInventoryKeepsPaymentInspectionAndStopsBuilder(t *testing.T) {
	setting := settingsmessaging.NotificationCenterDefaultSetting()
	setting.InventoryAlertEnabled = false
	setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123"}}
	dashboardSetting := settingsstorefront.DashboardSetting{Alert: settingsstorefront.DashboardAlertSetting{PendingPaymentOrdersThreshold: 1, PaymentsFailedThreshold: 1}}
	rows := []dashboard.InventoryAlertRow{{ProductID: 1, AlertType: constants.NotificationAlertTypeLowStockProducts}, {ProductID: 2, AlertType: constants.NotificationAlertTypeOutOfStockProducts}}
	if got := format.BuildInventoryAlertDispatchPayloads(setting, dashboardSetting, queue.NotificationDispatchPayload{}, rows); len(got) != 0 {
		t.Fatal("disabled builder produced inventory alerts")
	}
	enabled := setting
	enabled.InventoryAlertEnabled = true
	if got := format.BuildInventoryAlertDispatchPayloads(enabled, dashboardSetting, queue.NotificationDispatchPayload{}, rows); len(got) != 2 {
		t.Fatal("enabled builder lost inventory alerts")
	}
	reader := &inventorySwitchDashboardSpy{}
	tg := &loginTelegramSpy{}
	svc := NewService(notificationSettingsStub{notification: setting, dashboard: dashboardSetting}, nil, nil, reader, nil, tg)
	if err := svc.Dispatch(context.Background(), queue.NotificationDispatchPayload{EventType: constants.NotificationEventExceptionAlertCheck, Force: true}); err != nil {
		t.Fatal(err)
	}
	if reader.inventoryCalls != 0 || reader.paymentCalls != 1 || len(tg.messages) != 2 {
		t.Fatalf("inventory=%d payment=%d sent=%d", reader.inventoryCalls, reader.paymentCalls, len(tg.messages))
	}
}
