package format

import (
	"fmt"
	"github.com/dujiao-next/internal/constants"
	"regexp"
	"strings"
	"time"

	settingsmessaging "github.com/dujiao-next/internal/modules/settings/schema/messaging"
	"github.com/dujiao-next/internal/queue"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

var notificationTemplateVariablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)

func BuildTemplateVariables(payload queue.NotificationDispatchPayload) map[string]interface{} {
	data := CloneVariables(payload.Data)
	if data == nil {
		data = map[string]interface{}{}
	}
	data["event_type"] = strings.ToLower(strings.TrimSpace(payload.EventType))
	data["biz_type"] = strings.TrimSpace(payload.BizType)
	data["biz_id"] = fmt.Sprintf("%d", payload.BizID)
	data["occurred_at"] = time.Now().Format("2006-01-02 15:04:05")
	return data
}

func RenderTemplate(tmpl string, variables map[string]interface{}) string {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return ""
	}
	return notificationTemplateVariablePattern.ReplaceAllStringFunc(tmpl, func(matched string) string {
		submatch := notificationTemplateVariablePattern.FindStringSubmatch(matched)
		if len(submatch) != 2 {
			return matched
		}
		value, ok := variables[strings.TrimSpace(submatch[1])]
		if !ok {
			return ""
		}
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	})
}

func ResolveLocale(locale, fallback string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale = strings.TrimSpace(fallback)
	}
	return normalizeNotificationLocale(locale)
}

func ComposeTelegramMessage(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" {
		return body
	}
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

func JSONToMap(data jsonmap.JSON) map[string]interface{} {
	if data == nil {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(data))
	for key, value := range data {
		result[key] = value
	}
	return result
}

func CloneVariables(data map[string]interface{}) map[string]interface{} {
	if len(data) == 0 {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(data))
	for key, value := range data {
		result[key] = value
	}
	return result
}

func normalizeNotificationLocale(locale string) string {
	return settingsmessaging.NormalizeNotificationLocale(locale)
}

func normalizeNotificationInventoryAlertInterval(seconds int) int {
	return settingsmessaging.NormalizeNotificationInventoryAlertInterval(seconds)
}

func normalizeNotificationPaymentOrderAlertInterval(seconds int) int {
	return settingsmessaging.NormalizeNotificationPaymentOrderAlertInterval(seconds)
}

// AdminLoginTemplate is deliberately independent of editable business templates.
// The existing exception-alert switch controls this Telegram-only security reminder.
func AdminLoginTemplate(locale string) settingsmessaging.NotificationLocalizedTemplate {
	switch locale {
	case constants.LocaleEnUS:
		return settingsmessaging.NotificationLocalizedTemplate{Title: "Administrator sign-in succeeded", Body: "Administrator: {{admin_username}}\nTime (UTC): {{login_at}}\nIP: {{client_ip}}\nMethod: {{login_method}}\nIf this was not you, secure the administrator account immediately."}
	case constants.LocaleZhTW:
		return settingsmessaging.NotificationLocalizedTemplate{Title: "管理員登入成功提醒", Body: "管理員：{{admin_username}}\n時間（UTC）：{{login_at}}\nIP：{{client_ip}}\n方式：{{login_method}}\n如非本人操作，請立即檢查管理員帳號安全。"}
	default:
		return settingsmessaging.NotificationLocalizedTemplate{Title: "管理员登录成功提醒", Body: "管理员：{{admin_username}}\n时间（UTC）：{{login_at}}\nIP：{{client_ip}}\n方式：{{login_method}}\n如非本人操作，请立即检查管理员账号安全。"}
	}
}

// AdminSecurityTemplate never interpolates credentials or editable business templates.
func AdminSecurityTemplate(event, locale string) settingsmessaging.NotificationLocalizedTemplate {
	if event == constants.NotificationEventAdminLoginSuccess {
		return AdminLoginTemplate(locale)
	}
	if event == constants.NotificationEventAdminLoginFailed {
		if locale == constants.LocaleEnUS {
			return settingsmessaging.NotificationLocalizedTemplate{Title: "Repeated administrator sign-in failures", Body: "IP: {{client_ip}}\nFailures: {{failure_count}} within {{window_minutes}} minutes\nLast method: {{login_method}}\nTime (UTC): {{occurred_at}}\nAccount existence is not disclosed."}
		}
		return settingsmessaging.NotificationLocalizedTemplate{Title: "管理员连续登录失败告警", Body: "IP：{{client_ip}}\n{{window_minutes}} 分钟内失败次数：{{failure_count}}\n最近验证方式：{{login_method}}\n时间（UTC）：{{occurred_at}}\n请检查是否存在异常登录；不披露尝试账号是否存在。"}
	}
	return settingsmessaging.NotificationLocalizedTemplate{Title: "管理员收款配置变更告警", Body: "管理员：{{admin_username}}\n操作：{{action}}\n支付渠道：{{object_id}}\n变更字段：{{changed_fields}}\nIP：{{client_ip}}\n时间（UTC）：{{occurred_at}}\n如非授权操作，请立即检查收款配置。"}
}
