package smtp

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

func TestOrderCardPreservesPlainTextAndAttachment(t *testing.T) {
	body := "订单号：EXAMPLE\n交付内容：\n<script>alert(1)</script>\nhttps://example.com/a"
	data, err := buildOrderEmailMessage("shop@example.com", "buyer@example.com", "订单已交付", body, "交付.txt", "attachment data", "")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mixed := multipart.NewReader(msg.Body, params["boundary"])
	first, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	_, params, err = mime.ParseMediaType(first.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	alt := multipart.NewReader(first, params["boundary"])
	plain, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, plain))
	if err != nil || string(decoded) != body {
		t.Fatalf("plain text changed: %q %v", decoded, err)
	}
	card, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, card))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoded), "<script>") || !strings.Contains(string(decoded), "&lt;script&gt;") || !strings.Contains(string(decoded), "EXAMPLE") || !strings.Contains(string(decoded), `href="https://example.com/a"`) {
		t.Fatal("unsafe or incomplete HTML")
	}
	attachment, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
	if err != nil || string(decoded) != "attachment data" {
		t.Fatal("attachment changed")
	}
	_, attrs, err := mime.ParseMediaType(attachment.Header.Get("Content-Disposition"))
	if err != nil || attrs["filename"] != "交付.txt" {
		t.Fatalf("filename: %v %v", attrs, err)
	}
}

func TestOrderCardEscapesUntrustedTextAndKeepsSafeLinks(t *testing.T) {
	got := linkOrderEmailURLs(`<img src=x onerror=alert(1)> https://example.com/a?x=1&y=2" onclick="x`)
	if strings.Contains(got, "<img") || strings.Contains(got, `onclick="x`) || !strings.Contains(got, `href="https://example.com/a?x=1&amp;y=2"`) {
		t.Fatalf("unexpected markup: %s", got)
	}
}
