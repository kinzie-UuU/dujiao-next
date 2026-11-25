package smtp

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/i18n"
	"github.com/dujiao-next/internal/shared/mailbrand"
)

func TestVerifyCardBrandBoundary(t *testing.T) {
	for _, raw := range []string{"https://uuuaigc.com", "https://uuuaigc.com/", "https://www.uuuaigc.com/", " https://uuuaigc.com/ "} {
		if !usesUuuaigcVerifyCard(mailbrand.Brand{SiteURL: raw}) {
			t.Errorf("main storefront rejected: %q", raw)
		}
	}
	for _, raw := range []string{"", "https://shop.example.test", "http://uuuaigc.com", "//uuuaigc.com", "https://uuuaigc.com.evil.test", "https://eviluuuaigc.com", "https://uuuaigc.com@evil.test", "https://evil.test@uuuaigc.com", "https://uuuaigc.com:443", "https://uuuaigc.com/shop", "https://uuuaigc.com/?tenant=other", "https://uuuaigc.com/#other", "https://uuuaigc.com./", "https://sub.uuuaigc.com", "javascript:alert(1)", "https://uuuaigc.com\\@evil.test", "https://%75uuaigc.com"} {
		if usesUuuaigcVerifyCard(mailbrand.Brand{SiteName: "UuUAiGC", SiteURL: raw}) {
			t.Errorf("non-main URL accepted: %q", raw)
		}
	}
}

func TestVerifyCardLocalesAndPurposes(t *testing.T) {
	purposes := []string{constants.VerifyPurposeRegister, constants.VerifyPurposeReset, constants.VerifyPurposeTelegramBind, constants.VerifyPurposeChangeEmailOld, constants.VerifyPurposeChangeEmailNew}
	cases := []struct {
		locale, lang, instruction, safety string
		titles                            []string
	}{
		{i18n.LocaleZH, "zh-CN", "请返回刚才的页面", "请勿转发或透露给他人", []string{"注册验证码", "重置密码验证码", "Telegram 绑定验证码", "更换邮箱验证码", "更换邮箱验证码"}},
		{i18n.LocaleTW, "zh-TW", "請返回剛才的頁面", "請勿轉寄或透露給他人", []string{"註冊驗證碼", "重置密碼驗證碼", "Telegram 綁定驗證碼", "更換郵箱驗證碼", "更換郵箱驗證碼"}},
		{i18n.LocaleEN, "en", "Return to the page", "Do not forward or share", []string{"Registration Code", "Password Reset Code", "Telegram Binding Code", "Change Email Code", "Change Email Code"}},
	}
	for _, tc := range cases {
		for n, purpose := range purposes {
			t.Run(tc.locale+"/"+purpose, func(t *testing.T) {
				card, err := renderVerifyCard("012345", purpose, tc.locale)
				if err != nil {
					t.Fatal(err)
				}
				for _, wanted := range []string{`lang="` + tc.lang + `"`, tc.titles[n], tc.instruction, tc.safety, "012345", `src="cid:uuuaigc-verification-logo"`} {
					if !strings.Contains(card, wanted) {
						t.Errorf("missing localized content %q", wanted)
					}
				}
				if strings.Contains(card, `src="http`) || strings.Contains(card, "<script") || strings.Contains(card, "分钟") || strings.Contains(card, "minutes") {
					t.Fatal("remote image, script, or invented expiry in verification card")
				}
			})
		}
	}
}

func TestVerifyCardEscapesCode(t *testing.T) {
	card, err := renderVerifyCard(`<img src=x onerror="alert(1)">&012345`, constants.VerifyPurposeReset, i18n.LocaleZH)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(card, `<img src=x`) || strings.Contains(card, `onerror="alert(1)"`) || !strings.Contains(card, "&lt;img") || !strings.Contains(card, "&amp;012345") {
		t.Fatal("code was not safely escaped as text")
	}
}

func decodeVerifyPart(t *testing.T, p *multipart.Part, expected string) []byte {
	t.Helper()
	kind, _, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
	if err != nil || kind != expected {
		t.Fatalf("part type = %q, err = %v", kind, err)
	}
	if p.Header.Get("Content-Transfer-Encoding") != "base64" {
		t.Fatal("missing base64 encoding")
	}
	raw, err := io.ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 76 {
			t.Fatal("base64 MIME line exceeds 76 bytes")
		}
	}
	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestVerifyCardMessagePreservesTextHeadersAndEmbeddedLogo(t *testing.T) {
	from := buildFromAddress("mailer@example.test", "UuUAiGC")
	subject := "UuUAiGC - 重置密码验证码"
	body := "您的验证码是：012345\r\n\r\n请勿泄露。\n特殊字符：<>&\n"
	raw, err := buildVerifyCardMessage(from, "buyer@example.test", subject, body, "012345", constants.VerifyPurposeReset, i18n.LocaleZH, "support@example.test")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	decodedSubject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || decodedSubject != subject {
		t.Fatal("subject changed")
	}
	for k, v := range map[string]string{"From": from, "To": "buyer@example.test", "Reply-To": "support@example.test", "MIME-Version": "1.0"} {
		if msg.Header.Get(k) != v {
			t.Errorf("header %s changed", k)
		}
	}
	if _, err := msg.Header.Date(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(msg.Header.Get("Message-ID"), "@example.test>") {
		t.Fatal("message identity missing")
	}
	kind, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/related" || params["type"] != "multipart/alternative" {
		t.Fatal("missing related MIME envelope")
	}
	related := multipart.NewReader(msg.Body, params["boundary"])
	root, err := related.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	kind, params, err = mime.ParseMediaType(root.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/alternative" {
		t.Fatal("missing alternative MIME envelope")
	}
	alt := multipart.NewReader(root, params["boundary"])
	plain, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeVerifyPart(t, plain, "text/plain"); string(got) != body {
		t.Fatal("original plaintext changed")
	}
	html, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	card := decodeVerifyPart(t, html, "text/html")
	if !bytes.Contains(card, []byte("012345")) || !bytes.Contains(card, []byte(`cid:uuuaigc-verification-logo`)) {
		t.Fatal("HTML lacks code or inline logo")
	}
	if _, err = alt.NextPart(); err != io.EOF {
		t.Fatal("unexpected extra alternative")
	}
	logo, err := related.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if logo.Header.Get("Content-ID") != "<uuuaigc-verification-logo>" {
		t.Fatal("logo CID does not match HTML")
	}
	disposition, _, err := mime.ParseMediaType(logo.Header.Get("Content-Disposition"))
	if err != nil || disposition != "inline" {
		t.Fatal("logo is not inline")
	}
	image := decodeVerifyPart(t, logo, "image/png")
	if !bytes.Equal(image, verifyCardLogo) {
		t.Fatal("embedded logo bytes changed")
	}
	if _, err := png.Decode(bytes.NewReader(image)); err != nil {
		t.Fatalf("embedded logo is not a valid PNG: %v", err)
	}
	if _, err = related.NextPart(); err != io.EOF {
		t.Fatal("unexpected extra related part")
	}
}
