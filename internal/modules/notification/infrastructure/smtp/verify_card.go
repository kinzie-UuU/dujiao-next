package smtp

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/dujiao-next/internal/i18n"
	"github.com/dujiao-next/internal/shared/mailbrand"
)

//go:embed assets/uuuaigc-logo.png
var verifyCardLogo []byte

// Only the main storefront opts into this brand-specific presentation.
func usesUuuaigcVerifyCard(brand mailbrand.Brand) bool {
	u, err := url.Parse(strings.TrimSpace(brand.SiteURL))
	return err == nil && u.Scheme == "https" && (u.Host == "uuuaigc.com" || u.Host == "www.uuuaigc.com") && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}

var verifyCardTemplate = template.Must(template.New("verification").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>{{.Title}}</title></head>
<body style="margin:0;background:#f3f4f0;color:#080e17;font-family:'Helvetica Neue','PingFang SC','Microsoft YaHei',sans-serif">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#f3f4f0;table-layout:fixed"><tr><td align="center" style="padding:32px 16px">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:560px;table-layout:fixed;background:#fff;border:1px solid #dfe3d8;border-radius:16px;overflow:hidden">
<tr><td height="8" bgcolor="#e6ff57" style="height:8px;font-size:0;line-height:0;border-radius:16px 16px 0 0">&nbsp;</td></tr>
<tr><td style="padding:24px 24px 20px;border-bottom:1px solid #edf0e7"><img src="cid:uuuaigc-verification-logo" width="119" height="44" alt="UuUAiGC" style="display:block;width:119px;max-width:100%;height:auto;border:0;color:#080e17;font-size:20px"></td></tr>
<tr><td style="padding:28px 24px 30px"><p style="margin:0 0 10px;font-size:12px;letter-spacing:2px;color:#626a59">{{.Eyebrow}}</p><h1 style="margin:0 0 14px;font-size:26px;line-height:1.4;font-weight:500;color:#080e17">{{.Title}}</h1><p style="margin:0 0 24px;font-size:15px;line-height:1.8;color:#4d5648">{{.Instruction}}</p>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#f5fbd8;border:1px solid #bbc98a;border-radius:12px"><tr><td align="center" style="padding:20px 8px 22px"><p style="margin:0 0 8px;font-size:12px;color:#515e2c">{{.CodeLabel}}</p><div dir="ltr" style="font-family:'Courier New',monospace;font-size:34px;font-weight:bold;line-height:1.4;letter-spacing:4px;color:#080e17;word-break:break-all">{{.Code}}</div></td></tr></table>
<p style="margin:22px 0 0;font-size:13px;line-height:1.8;color:#626a59">{{.Safety}}</p></td></tr>
<tr><td style="padding:18px 24px;background:#080e17;border-radius:0 0 16px 16px"><p style="margin:0;font-size:13px;line-height:1.7;color:#edf1e6">{{.Ignore}}</p></td></tr>
</table><p style="margin:20px 0 0;font-size:12px;line-height:1.8;color:#626a59"><a href="https://uuuaigc.com" style="color:#4d5648;text-decoration:underline;word-break:break-all">uuuaigc.com</a></p>
</td></tr></table></body></html>`))

func renderVerifyCard(code, purpose, locale string) (string, error) {
	title, _ := buildVerifyCodeContent("", purpose, locale)
	data := struct{ Lang, Title, Eyebrow, Instruction, CodeLabel, Code, Safety, Ignore string }{
		Lang: "zh-CN", Title: title, Eyebrow: "账户安全", Instruction: "请返回刚才的页面，输入以下验证码继续操作。", CodeLabel: "本次验证码", Code: code,
		Safety: "验证码仅用于本次操作，请勿转发或透露给他人。", Ignore: "如果不是您本人操作，请忽略这封邮件。",
	}
	switch normalizeLocale(locale) {
	case i18n.LocaleTW:
		data.Lang, data.Eyebrow, data.Instruction, data.CodeLabel = "zh-TW", "帳戶安全", "請返回剛才的頁面，輸入以下驗證碼繼續操作。", "本次驗證碼"
		data.Safety, data.Ignore = "驗證碼僅用於本次操作，請勿轉寄或透露給他人。", "如果不是您本人操作，請忽略這封郵件。"
	case i18n.LocaleEN:
		data.Lang, data.Eyebrow, data.Instruction, data.CodeLabel = "en", "ACCOUNT SECURITY", "Return to the page you were using and enter this code to continue.", "Your verification code"
		data.Safety, data.Ignore = "Use this code only for this request. Do not forward or share it with anyone.", "If you did not request this, you can ignore this email."
	}
	var buf bytes.Buffer
	if err := verifyCardTemplate.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// A related MIME container embeds the real logo; no remote image or tracking request.
// The alternative retains the existing plain text verbatim for text-only clients.
func buildVerifyCardMessage(from, to, subject, body, code, purpose, locale, replyTo string) ([]byte, error) {
	card, err := renderVerifyCard(code, purpose, locale)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	writeStandardHeaders(&buf, from, to, subject, replyTo)
	related := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/related; type=\"multipart/alternative\"; boundary=%q\r\n\r\n", related.Boundary())
	var altBuf bytes.Buffer
	alt := multipart.NewWriter(&altBuf)
	for _, p := range []struct{ kind, body string }{{"text/plain; charset=UTF-8", body}, {"text/html; charset=UTF-8", card}} {
		if err := addVerifyMIMEPart(alt, textproto.MIMEHeader{"Content-Type": {p.kind}}, []byte(p.body)); err != nil {
			return nil, err
		}
	}
	if err := alt.Close(); err != nil {
		return nil, err
	}
	h := textproto.MIMEHeader{"Content-Type": {fmt.Sprintf("multipart/alternative; boundary=%q", alt.Boundary())}}
	w, err := related.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(altBuf.Bytes()); err != nil {
		return nil, err
	}
	h = textproto.MIMEHeader{"Content-Type": {"image/png"}, "Content-ID": {"<uuuaigc-verification-logo>"}, "Content-Disposition": {`inline; filename="uuuaigc.png"`}}
	if err := addVerifyMIMEPart(related, h, verifyCardLogo); err != nil {
		return nil, err
	}
	if err := related.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addVerifyMIMEPart(writer *multipart.Writer, h textproto.MIMEHeader, body []byte) error {
	h.Set("Content-Transfer-Encoding", "base64")
	w, err := writer.CreatePart(h)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(body)
	for len(encoded) > 76 {
		if _, err := fmt.Fprint(w, encoded[:76]+"\r\n"); err != nil {
			return err
		}
		encoded = encoded[76:]
	}
	_, err = fmt.Fprint(w, encoded)
	return err
}
