package paymentbootstrap

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/app/container"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/logger"
	auditapp "github.com/dujiao-next/internal/modules/auditlog/application"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"regexp"
	"strings"
	"time"

	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	"github.com/dujiao-next/internal/constants"
	notificationcontract "github.com/dujiao-next/internal/modules/notification/contract"
	paymenttransport "github.com/dujiao-next/internal/modules/payment/transport/http"
	paymentcallbacktransport "github.com/dujiao-next/internal/modules/payment/transport/http/callback"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

type adminQueryAdapter struct {
	payments *paymentapp.PaymentService
}

func (a adminQueryAdapter) ListPayments(filter paymenttransport.AdminPaymentListFilter) ([]paymentdomain.Payment, int64, error) {
	return a.payments.ListPayments(paymentcontract.ListFilter{
		Page:         filter.Page,
		PageSize:     filter.PageSize,
		UserID:       filter.UserID,
		OrderID:      filter.OrderID,
		ChannelID:    filter.ChannelID,
		ProviderType: filter.ProviderType,
		ChannelType:  filter.ChannelType,
		Status:       filter.Status,
		CreatedFrom:  filter.CreatedFrom,
		CreatedTo:    filter.CreatedTo,
		SkipCount:    filter.SkipCount,
		Lightweight:  filter.Lightweight,
	})
}

func (a adminQueryAdapter) GetPayment(id uint) (*paymentdomain.Payment, error) {
	payment, err := a.payments.GetPayment(id)
	return payment, mapTransportError(err)
}

func (a adminQueryAdapter) ConfirmManualPayment(id uint, evidence ...paymentdomain.ManualPaymentEvidence) (*paymentdomain.Payment, error) {
	payment, err := a.payments.ConfirmManualPayment(id, evidence...)
	return payment, mapTransportError(err)
}

type adminChannelLookupAdapter struct {
	channels paymentcontract.ChannelStore
}

func (a adminChannelLookupAdapter) ListByIDs(ids []uint) ([]paymentdomain.PaymentChannel, error) {
	return a.channels.ListByIDs(ids)
}

type adminOrderLookupAdapter struct {
	orders ordercontract.Store
}

func (a adminOrderLookupAdapter) GetByIDs(ids []uint) ([]orderdomain.Order, error) {
	return a.orders.GetByIDs(ids)
}

type adminRechargeLookupAdapter struct {
	wallets walletcontract.Repository
}

func (a adminRechargeLookupAdapter) GetRechargeOrdersByPaymentIDs(paymentIDs []uint) ([]walletdomain.RechargeOrder, error) {
	if a.wallets == nil {
		return nil, nil
	}
	return a.wallets.GetRechargeOrdersByPaymentIDs(paymentIDs)
}

type adminChannelCatalogAdapter struct {
	payments *paymentapp.PaymentService
	channels paymentcontract.ChannelStore
}

func (a adminChannelCatalogAdapter) ValidateChannel(channel *paymentdomain.PaymentChannel) error {
	return mapTransportError(a.payments.ValidateChannel(channel))
}

func (a adminChannelCatalogAdapter) GetChannel(id uint) (*paymentdomain.PaymentChannel, error) {
	channel, err := a.payments.GetChannel(id)
	return channel, mapTransportError(err)
}

func (a adminChannelCatalogAdapter) ListChannels(filter paymenttransport.AdminChannelListFilter) ([]paymentdomain.PaymentChannel, int64, error) {
	return a.payments.ListChannels(paymentcontract.ChannelListFilter{
		Page:         filter.Page,
		PageSize:     filter.PageSize,
		ProviderType: filter.ProviderType,
		ChannelType:  filter.ChannelType,
		ActiveOnly:   filter.ActiveOnly,
	})
}

func (a adminChannelCatalogAdapter) Create(channel *paymentdomain.PaymentChannel) error {
	return a.channels.Create(channel)
}

func (a adminChannelCatalogAdapter) Update(channel *paymentdomain.PaymentChannel) error {
	return a.channels.Update(channel)
}

func (a adminChannelCatalogAdapter) Delete(id uint) error {
	return a.channels.Delete(id)
}

type webhookServiceAdapter struct {
	payments *paymentapp.PaymentService
}

type callbackServiceAdapter struct {
	payments *paymentapp.PaymentService
}

func (a callbackServiceAdapter) HandleSyncCallback(channel *paymentdomain.PaymentChannel, form map[string][]string, body []byte) (*paymentdomain.Payment, error) {
	return a.payments.HandleSyncCallback(channel, form, body)
}

func (a callbackServiceAdapter) HandleWechatWebhook(input paymentcallbacktransport.WechatWebhookInput) (*paymentdomain.Payment, string, error) {
	payment, eventType, err := a.payments.HandleWechatWebhook(paymentapp.WebhookCallbackInput{
		ChannelID: input.ChannelID,
		Headers:   input.Headers,
		Body:      input.Body,
		Context:   input.Context,
	})
	return payment, eventType, mapTransportError(err)
}

func (a webhookServiceAdapter) HandlePaypalWebhook(input paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	payment, eventType, err := a.payments.HandlePaypalWebhook(paymentapp.WebhookCallbackInput{
		ChannelID: input.ChannelID,
		Headers:   input.Headers,
		Body:      input.Body,
		Context:   input.Context,
	})
	return payment, eventType, mapTransportError(err)
}

func (a webhookServiceAdapter) HandleStripeWebhook(input paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	payment, eventType, err := a.payments.HandleStripeWebhook(paymentapp.WebhookCallbackInput{
		ChannelID: input.ChannelID,
		Headers:   input.Headers,
		Body:      input.Body,
		Context:   input.Context,
	})
	return payment, eventType, mapTransportError(err)
}

func (a webhookServiceAdapter) HandleDujiaoPayWebhook(input paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	payment, eventType, err := a.payments.HandleDujiaoPayWebhook(paymentapp.WebhookCallbackInput{
		ChannelID: input.ChannelID,
		Headers:   input.Headers,
		Body:      input.Body,
		Context:   input.Context,
	})
	return payment, eventType, mapTransportError(err)
}

type exceptionAlerterAdapter struct {
	notifications notificationcontract.NotificationEnqueuer
}

func (a exceptionAlerterAdapter) EnqueuePaymentExceptionAlert(method, path, clientIP string, data jsonmap.JSON) error {
	if a.notifications == nil {
		return nil
	}
	payload := jsonmap.JSON{
		"source":      constants.NotificationBizTypePaymentCallback,
		"method":      method,
		"path":        path,
		"client_ip":   clientIP,
		"occurred_at": time.Now().Format("2006-01-02 15:04:05"),
	}
	for key, value := range data {
		payload[key] = value
	}
	return a.notifications.Enqueue(notificationcontract.EnqueueInput{
		EventType: constants.NotificationEventExceptionAlert,
		BizType:   constants.NotificationBizTypePaymentCallback,
		BizID:     0,
		Data:      payload,
	})
}

var errPaymentStepUp = errors.New("payment step-up unavailable or invalid")
var paymentCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

type paymentTOTPVerifier interface{ VerifyChallengeCode(uint, string) error }
type paymentStepUp struct {
	redis   *redis.Client
	totp    paymentTOTPVerifier
	key     string
	ready   func(context.Context) error
	audit   func(uint) error
	changed func(uint, string, string, uint, []string)
}

// Verify fails closed: no local fallback, recovery codes or reusable step-up tokens.
func (g *paymentStepUp) Verify(ctx context.Context, id uint, code string) error {
	if g == nil || g.redis == nil || g.totp == nil || g.key == "" || id == 0 || !paymentCodePattern.MatchString(code) {
		return errPaymentStepUp
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	attempts, err := g.redis.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],300) end; return n`, []string{fmt.Sprintf("payment:stepup:attempts:%d", id)}).Int64()
	if err != nil || attempts > 10 {
		return errPaymentStepUp
	}
	if err = g.totp.VerifyChallengeCode(id, code); err != nil {
		return errPaymentStepUp
	}
	if g.ready == nil || g.ready(ctx) != nil {
		return errPaymentStepUp
	}
	mac := hmac.New(sha256.New, []byte(g.key))
	fmt.Fprintf(mac, "%d:%s", id, code)
	// 120 seconds exceeds the entire 90-second acceptance window (skew +/-1).
	fresh, err := g.redis.SetNX(ctx, "payment:stepup:used:"+hex.EncodeToString(mac.Sum(nil)), "1", 120*time.Second).Result()
	if err != nil || !fresh {
		return errPaymentStepUp
	}
	// An intent record also proves the audit store is writable before mutation.
	if g.audit == nil || g.audit(id) != nil {
		return errPaymentStepUp
	}
	return nil
}
func (g *paymentStepUp) Changed(id uint, ip, action string, channel uint, fields []string) {
	g.changed(id, ip, action, channel, fields)
}

func newPaymentStepUp(c *container.Container) *paymentStepUp {
	g := &paymentStepUp{redis: cache.Client(), totp: c.TOTPService, key: c.Config.JWT.SecretKey}
	// The queue may use a different Redis DB or host from the cache.
	queueRedis := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", c.Config.Queue.Host, c.Config.Queue.Port), Password: c.Config.Queue.Password, DB: c.Config.Queue.DB, MaxRetries: -1, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	g.ready = func(ctx context.Context) error {
		if c.QueueClient == nil || !c.QueueClient.Enabled() || c.NotificationService == nil || c.AuthzAuditService == nil {
			return errPaymentStepUp
		}
		setting, err := c.SettingService.GetNotificationCenterSetting()
		if err != nil || !setting.Scenes.ExceptionAlert || !setting.Channels.Telegram.Enabled || len(setting.Channels.Telegram.Recipients) == 0 {
			return errPaymentStepUp
		}
		bot, err := c.SettingService.GetTelegramAuthSetting(c.Config.TelegramAuth)
		if err != nil || strings.TrimSpace(bot.BotToken) == "" {
			return errPaymentStepUp
		}
		return queueRedis.Ping(ctx).Err()
	}
	g.audit = func(id uint) error {
		return c.AuthzAuditService.Record(auditapp.AuthzRecord{OperatorAdminID: id, Action: "payment_channel.change_authorized", Object: "payment_channel"})
	}
	g.changed = func(id uint, ip, action string, channel uint, fields []string) {
		actor, err := c.AdminStore.GetByID(id)
		username := ""
		if err == nil && actor != nil {
			username = actor.Username
		}
		eventID := uuid.NewString()
		data := jsonmap.JSON{"admin_username": username, "client_ip": ip, "occurred_at": time.Now().UTC().Format(time.RFC3339), "object_id": fmt.Sprint(channel), "changed_fields": strings.Join(fields, ", "), "security_event_id": eventID, "action": action}
		record := auditapp.AuthzRecord{OperatorAdminID: id, OperatorUsername: username, Action: "payment_channel." + action, Object: fmt.Sprintf("payment_channel/%d", channel), RequestID: eventID, Detail: data}
		if err := c.AuthzAuditService.Record(record); err != nil {
			logger.Z().Error("payment_change_audit_failed", zap.String("event_id", eventID), zap.Uint("channel_id", channel))
		}
		// Security event enqueue has a hard two-second deadline in the queue client.
		// A failed enqueue remains discoverable in the durable audit trail.
		if err := c.NotificationService.Enqueue(notificationcontract.EnqueueInput{EventType: constants.NotificationEventAdminPaymentConfigChanged, BizType: "payment_channel", BizID: channel, Data: data}); err != nil {
			record.Action = "payment_channel.notification_enqueue_failed"
			if auditErr := c.AuthzAuditService.Record(record); auditErr != nil {
				logger.Z().Error("payment_notice_failure_audit_failed", zap.String("event_id", eventID))
			}
			logger.Z().Error("payment_change_notification_enqueue_failed", zap.String("event_id", eventID), zap.Uint("channel_id", channel))
		}
	}
	return g
}
