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
	return dashboard.PaymentOrderAlertCountsRow{PendingPaymentOrders: 2, PaymentsFailed: 2, FailedPaymentIDs: []uint{1, 2}}, nil
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

func TestSecurityEventTimeUsesOriginalInstantNotDelayedDispatch(t *testing.T) {
	for _, raw := range []string{"2026-11-04T12:34:56Z", "2026-11-04T20:34:56+08:00", "2026-11-04T07:34:56-05:00"} {
		p := queue.NotificationDispatchPayload{EventType: constants.NotificationEventAdminLoginFailed, Data: map[string]interface{}{"occurred_at": raw}}
		got := format.BuildTemplateVariables(p)["occurred_at"]
		if got != "2026-11-04T12:34:56Z" {
			t.Fatalf("%q: got %v", raw, got)
		}
		body := format.RenderTemplate(format.AdminSecurityTemplate(p.EventType, constants.LocaleEnUS).Body, format.BuildTemplateVariables(p))
		if !strings.Contains(body, "Time (UTC): 2026-11-04T12:34:56Z") {
			t.Fatalf("fixed template lost event time: %q", body)
		}
	}
}

func TestSecurityEventTimeMissingOrAmbiguousIsUnknown(t *testing.T) {
	for _, raw := range []interface{}{nil, "", "2026-11-04 12:34:56", "bad", 42} {
		p := queue.NotificationDispatchPayload{EventType: constants.NotificationEventAdminPaymentConfigChanged, Data: map[string]interface{}{"occurred_at": raw}}
		if got := format.BuildTemplateVariables(p)["occurred_at"]; got != "—" {
			t.Fatalf("%v: got %v", raw, got)
		}
	}
	p := queue.NotificationDispatchPayload{EventType: constants.NotificationEventAdminLoginFailed}
	if got := format.BuildTemplateVariables(p)["occurred_at"]; got != "—" {
		t.Fatalf("missing: got %v", got)
	}
}

func TestOtherTemplatesRetainDispatchTimeSemantics(t *testing.T) {
	p := queue.NotificationDispatchPayload{EventType: constants.NotificationEventOrderPaidSuccess, Data: map[string]interface{}{"occurred_at": "2000-01-01T00:00:00Z", "custom": "preserved"}}
	got := format.BuildTemplateVariables(p)
	if got["custom"] != "preserved" || strings.Contains(got["occurred_at"].(string), "2000-") {
		t.Fatalf("ordinary template changed: %+v", got)
	}
	if _, err := time.ParseInLocation("2006-01-02 15:04:05", got["occurred_at"].(string), time.Local); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentFailureAlertsOnlySendForNewUnresolvedAttempts(t *testing.T) {
	redis := miniredis.RunT(t)
	port, _ := strconv.Atoi(redis.Port())
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: redis.Host(), Port: port, Prefix: "payment-failure-test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	setting := settingsmessaging.NotificationCenterDefaultSetting()
	setting.PaymentOrderAlertIntervalSeconds = 1800
	setting.PaymentOrderAlertCheckSeconds = 86400
	setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123"}}
	tg := &loginTelegramSpy{}
	svc := NewService(notificationSettingsStub{notification: setting}, nil, nil, nil, nil, tg)
	payload := queue.NotificationDispatchPayload{EventType: constants.NotificationEventExceptionAlert, Data: map[string]interface{}{"alert_type_key": constants.NotificationAlertTypePaymentsFailed}}
	send := func(ids []uint, want int) {
		t.Helper()
		if err := svc.dispatchPaymentFailureAlert(context.Background(), setting, payload, ids); err != nil {
			t.Fatal(err)
		}
		if len(tg.messages) != want {
			t.Fatalf("ids %v: sent %d want %d", ids, len(tg.messages), want)
		}
	}
	disabled := setting
	disabled.Channels.Telegram.Enabled = false
	if err := svc.dispatchPaymentFailureAlert(context.Background(), disabled, payload, []uint{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if len(redis.Keys()) != 0 {
		t.Fatal("disabled delivery consumed failure identity")
	}
	send([]uint{1, 2, 3}, 1)
	send([]uint{1, 2, 3, 4}, 1) // normal interval still applies
	redis.FastForward(31 * time.Minute)
	send([]uint{1, 2, 3}, 1) // unchanged
	send([]uint{1, 2}, 1)    // resolved/aged-out attempts alone must not trigger
	send([]uint{1, 2, 4}, 2) // equal count, different failure is new
	redis.FastForward(31 * time.Minute)
	send([]uint{2, 4}, 2)
	tg.err = errors.New("temporary send failure")
	if err := svc.dispatchPaymentFailureAlert(context.Background(), setting, payload, []uint{2, 4, 5}); err == nil {
		t.Fatal("send failure swallowed")
	}
	tg.err = nil
	send([]uint{2, 4, 5}, 4) // failed send did not consume dedupe/interval
	redis.FastForward(31 * time.Minute)
	send([]uint{2, 4, 5}, 4)
}

type failureAlertSenderFunc func() error

func (f failureAlertSenderFunc) SendMessage(context.Context, string, string) error { return f() }

func TestPaymentFailureAlertCacheErrorsAreRetryable(t *testing.T) {
	redis := miniredis.RunT(t)
	port, _ := strconv.Atoi(redis.Port())
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: redis.Host(), Port: port, Prefix: "failure-cache-test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	setting := settingsmessaging.NotificationCenterDefaultSetting()
	setting.PaymentOrderAlertIntervalSeconds = 1800
	setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123"}}
	sends := 0
	failWrite := false
	sender := failureAlertSenderFunc(func() error {
		sends++
		if failWrite {
			redis.SetError("ERR cache write unavailable")
		}
		return nil
	})
	svc := NewService(notificationSettingsStub{notification: setting}, nil, nil, nil, nil, sender)
	payload := queue.NotificationDispatchPayload{EventType: constants.NotificationEventExceptionAlert, Data: map[string]interface{}{"alert_type_key": constants.NotificationAlertTypePaymentsFailed}}
	// A malformed/unreadable cache value must not be treated as an empty seen set.
	redis.Set("failure-cache-test:notification:payment_failures:seen", "not-json")
	if err := svc.dispatchPaymentFailureAlert(context.Background(), setting, payload, []uint{1}); err == nil {
		t.Fatal("cache read error swallowed")
	}
	if sends != 0 {
		t.Fatal("read failure sent a notification")
	}
	redis.Del("failure-cache-test:notification:payment_failures:seen")
	failWrite = true
	if err := svc.dispatchPaymentFailureAlert(context.Background(), setting, payload, []uint{1}); err == nil {
		t.Fatal("cache write error swallowed")
	}
	if sends != 1 {
		t.Fatalf("expected one send before cache write failed, got %d", sends)
	}
	redis.SetError("")
	failWrite = false
	// Redis was also unavailable during cleanup; bounded interval expiry allows retry.
	redis.FastForward(31 * time.Minute)
	if err := svc.dispatchPaymentFailureAlert(context.Background(), setting, payload, []uint{1}); err != nil {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatal("cache write failure consumed retry")
	}
	redis.FastForward(31 * time.Minute)
	if err := svc.dispatchPaymentFailureAlert(context.Background(), setting, payload, []uint{1}); err != nil {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatal("successful retry was not remembered")
	}
}

type ignoredPaymentDashboardSpy struct {
	inventorySwitchDashboardSpy
	counts dashboard.PaymentOrderAlertCountsRow
}

func (s *ignoredPaymentDashboardSpy) GetPaymentOrderAlertCounts(context.Context, time.Time, time.Time) (dashboard.PaymentOrderAlertCountsRow, error) {
	return s.counts, nil
}

func TestIgnoredPaymentFailuresFilterBeforeThresholdAndPreserveNewAlerts(t *testing.T) {
	redis := miniredis.RunT(t)
	port, _ := strconv.Atoi(redis.Port())
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: redis.Host(), Port: port, Prefix: "ignored-payment-test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Client().Close(); _ = cache.InitRedis(nil) })
	counts := dashboard.PaymentOrderAlertCountsRow{PendingPaymentOrders: 7, PaymentsFailed: 3, FailedPaymentIDs: []uint{660, 673, 677}}
	filtered := excludeIgnoredPaymentFailures(counts, []uint{660, 677})
	if filtered.PaymentsFailed != 1 || !reflect.DeepEqual(filtered.FailedPaymentIDs, []uint{673}) || filtered.PendingPaymentOrders != 7 {
		t.Fatalf("incorrect filtered counters: %+v", filtered)
	}
	if !reflect.DeepEqual(counts.FailedPaymentIDs, []uint{660, 673, 677}) || counts.PaymentsFailed != 3 {
		t.Fatal("filter mutated original statistics")
	}
	setting := settingsmessaging.NotificationCenterDefaultSetting()
	setting.InventoryAlertEnabled = false
	setting.IgnoredPaymentIDs = []uint{660, 677}
	setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123"}}
	ds := settingsstorefront.DashboardSetting{Alert: settingsstorefront.DashboardAlertSetting{PendingPaymentOrdersThreshold: 100, PaymentsFailedThreshold: 2}}
	reader := &ignoredPaymentDashboardSpy{counts: counts}
	tg := &loginTelegramSpy{}
	svc := NewService(notificationSettingsStub{notification: setting, dashboard: ds}, nil, nil, reader, nil, tg)
	payload := queue.NotificationDispatchPayload{EventType: constants.NotificationEventExceptionAlertCheck}
	if err := svc.Dispatch(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 0 {
		t.Fatal("unfiltered count triggered threshold")
	}
	// Lowering the threshold to one includes the unresolved 673, not the excluded attempts.
	ds.Alert.PaymentsFailedThreshold = 1
	svc = NewService(notificationSettingsStub{notification: setting, dashboard: ds}, nil, nil, reader, nil, tg)
	if err := svc.Dispatch(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 1 || !strings.Contains(tg.messages[0], "失败记录 1 笔") {
		t.Fatalf("remaining failure not notified: %v", tg.messages)
	}
	var seen []uint
	if _, err := cache.GetJSON(context.Background(), "notification:payment_failures:seen", &seen); err != nil || !reflect.DeepEqual(seen, []uint{673}) {
		t.Fatalf("dedupe contains excluded IDs: %v %v", seen, err)
	}
	redis.FastForward(31 * time.Minute)
	if err := svc.Dispatch(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 1 {
		t.Fatal("excluded historical attempts retriggered alert")
	}
	reader.counts.FailedPaymentIDs = []uint{660, 673, 677, 678}
	reader.counts.PaymentsFailed = 4
	if err := svc.Dispatch(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 2 || !strings.Contains(tg.messages[1], "失败记录 2 笔") {
		t.Fatalf("new real failure suppressed: %v", tg.messages)
	}
	if _, err := cache.GetJSON(context.Background(), "notification:payment_failures:seen", &seen); err != nil || !reflect.DeepEqual(seen, []uint{673, 678}) {
		t.Fatalf("new failure identity lost: %v %v", seen, err)
	}
	// The unrelated pending-order alert still uses its original count.
	redis.FastForward(31 * time.Minute)
	ds.Alert.PendingPaymentOrdersThreshold = 7
	svc = NewService(notificationSettingsStub{notification: setting, dashboard: ds}, nil, nil, reader, nil, tg)
	if err := svc.Dispatch(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 3 || !strings.Contains(tg.messages[2], "待支付订单 7 笔") {
		t.Fatal("payment exclusions altered pending-order alert")
	}
}
