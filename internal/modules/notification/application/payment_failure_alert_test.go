package application

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	settingsmessaging "github.com/dujiao-next/internal/modules/settings/schema/messaging"
	"github.com/dujiao-next/internal/queue"
)

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
