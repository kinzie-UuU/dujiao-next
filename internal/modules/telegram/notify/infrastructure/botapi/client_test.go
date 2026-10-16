package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	notifycontract "github.com/dujiao-next/internal/modules/telegram/notify/contract"
)

type failingTelegramTransport func(*http.Request) (*http.Response, error)

func (f failingTelegramTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type tokenEchoReadCloser struct{ token string }

func (r tokenEchoReadCloser) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failed: %s", r.token)
}
func (r tokenEchoReadCloser) Close() error { return nil }

func TestSendWithBotTokenErrorsNeverExposeCredentials(t *testing.T) {
	const token = "123456789:synthetic_TEST-secret"
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("uploads", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("uploads", "photo.png"), []byte("test image"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, attachment, token string
		transport               failingTelegramTransport
		nilContext              bool
	}{
		{name: "text transport URL", transport: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("transport echoed %s", req.URL.String())
		}},
		{name: "photo transport URL", attachment: "https://example.invalid/photo.png", transport: func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "POST", URL: req.URL.String(), Err: errors.New("unavailable")}
		}},
		{name: "document transport URL", attachment: "https://example.invalid/doc.txt", transport: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("transport %s", req.URL.String())
		}},
		{name: "multipart transport URL", attachment: "/uploads/photo.png", transport: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("transport %s", req.URL.String())
		}},
		{name: "response read echo", transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: tokenEchoReadCloser{token: token}}, nil
		}},
		{name: "HTTP error body echo", transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader("proxy URL https://api.telegram.org/bot" + token + "/sendMessage " + url.QueryEscape(token)))}, nil
		}},
		{name: "API error description echo", transport: func(*http.Request) (*http.Response, error) {
			body, _ := json.Marshal(map[string]interface{}{"ok": false, "description": token + " " + url.QueryEscape(token)})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}},
		{name: "malformed response", transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(token))}, nil
		}},
		{name: "request URL construction", token: token + "\ninvalid"},
		{name: "nil context construction", nilContext: true},
		{name: "missing local attachment", attachment: "/uploads/" + token + ".txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := test.transport
			if transport == nil {
				transport = func(*http.Request) (*http.Response, error) {
					t.Fatal("preparation failure reached network")
					return nil, nil
				}
			}
			client := NewWithHTTPClient(&http.Client{Transport: transport})
			ctx := context.Background()
			if test.nilContext {
				ctx = nil
			}
			secret := token
			if test.token != "" {
				secret = test.token
			}
			err := client.SendWithBotToken(ctx, secret, notifycontract.SendOptions{ChatID: "123456789", Message: "test", AttachmentURL: test.attachment})
			if !errors.Is(err, notifycontract.ErrNotifySendFailed) {
				t.Fatalf("send error classification lost: %v", err)
			}
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				for _, sensitive := range []string{token, url.QueryEscape(token), "synthetic_TEST-secret", "api.telegram.org/bot"} {
					if strings.Contains(fmt.Sprintf("%+v", cause), sensitive) {
						t.Fatalf("credential retained in returned error: %T", cause)
					}
				}
			}
		})
	}
}

type rewriteTelegramTransport struct {
	baseURL string
}

func (t rewriteTelegramTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := strings.TrimRight(t.baseURL, "/") + req.URL.Path
	rewritten, err := http.NewRequestWithContext(req.Context(), req.Method, target, req.Body)
	if err != nil {
		return nil, err
	}
	rewritten.Header = req.Header.Clone()
	return http.DefaultTransport.RoundTrip(rewritten)
}

func TestTelegramNotifyServiceSendWithBotTokenUploadsLocalAttachment(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	attachmentPath := filepath.Join("uploads", "telegram", "2026", "03", "demo.txt")
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o755); err != nil {
		t.Fatalf("mkdir attachment dir failed: %v", err)
	}
	if err := os.WriteFile(attachmentPath, []byte("hello telegram"), 0o644); err != nil {
		t.Fatalf("write attachment failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botbot-token/sendDocument" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		contentType := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("parse media type failed: %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("expected multipart/form-data, got %s", mediaType)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		documentFound := false
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read multipart part failed: %v", err)
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("read part body failed: %v", err)
			}
			if part.FormName() == "document" {
				documentFound = true
				if string(body) != "hello telegram" {
					t.Fatalf("unexpected attachment body: %s", string(body))
				}
				continue
			}
			fields[part.FormName()] = string(body)
		}
		if !documentFound {
			t.Fatalf("expected document part")
		}
		if fields["chat_id"] != "10001" {
			t.Fatalf("unexpected chat_id: %s", fields["chat_id"])
		}
		if fields["caption"] != "<b>Hello</b>" {
			t.Fatalf("unexpected caption: %s", fields["caption"])
		}
		if fields["parse_mode"] != "HTML" {
			t.Fatalf("unexpected parse_mode: %s", fields["parse_mode"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := NewWithHTTPClient(&http.Client{
		Transport: rewriteTelegramTransport{baseURL: server.URL},
	})

	err = svc.SendWithBotToken(context.Background(), "bot-token", notifycontract.SendOptions{
		ChatID:        "10001",
		Message:       "<b>Hello</b>",
		ParseMode:     "HTML",
		AttachmentURL: "/uploads/telegram/2026/03/demo.txt",
	})
	if err != nil {
		t.Fatalf("send with local attachment failed: %v", err)
	}
}

func TestTelegramNotifyServiceSendWithBotTokenUploadsLocalPhoto(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp dir failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	attachmentPath := filepath.Join("uploads", "telegram", "2026", "03", "demo.png")
	if err := os.MkdirAll(filepath.Dir(attachmentPath), 0o755); err != nil {
		t.Fatalf("mkdir attachment dir failed: %v", err)
	}
	if err := os.WriteFile(attachmentPath, []byte("png-data"), 0o644); err != nil {
		t.Fatalf("write attachment failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botbot-token/sendPhoto" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		contentType := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("parse media type failed: %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("expected multipart/form-data, got %s", mediaType)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		photoFound := false
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read multipart part failed: %v", err)
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("read part body failed: %v", err)
			}
			if part.FormName() == "photo" {
				photoFound = true
				if string(body) != "png-data" {
					t.Fatalf("unexpected photo body: %s", string(body))
				}
				continue
			}
			fields[part.FormName()] = string(body)
		}
		if !photoFound {
			t.Fatalf("expected photo part")
		}
		if fields["caption"] != "<b>Hello</b>" {
			t.Fatalf("unexpected caption: %s", fields["caption"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := NewWithHTTPClient(&http.Client{
		Transport: rewriteTelegramTransport{baseURL: server.URL},
	})

	err = svc.SendWithBotToken(context.Background(), "bot-token", notifycontract.SendOptions{
		ChatID:                "10001",
		Message:               "<b>Hello</b>",
		ParseMode:             "HTML",
		AttachmentURL:         "/uploads/telegram/2026/03/demo.png",
		AttachmentDisplayName: "demo.png",
	})
	if err != nil {
		t.Fatalf("send with local photo failed: %v", err)
	}
}

func TestTelegramNotifyServiceSendWithBotTokenSendsRemotePhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botbot-token/sendPhoto" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("expected application/json content type, got %s", r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body failed: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal payload failed: %v", err)
		}
		if payload["photo"] != "https://cdn.example.com/demo.jpg" {
			t.Fatalf("expected photo payload, got %q", payload["photo"])
		}
		if payload["caption"] != "<b>Hello</b>" {
			t.Fatalf("expected caption payload, got %q", payload["caption"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := NewWithHTTPClient(&http.Client{
		Transport: rewriteTelegramTransport{baseURL: server.URL},
	})

	err := svc.SendWithBotToken(context.Background(), "bot-token", notifycontract.SendOptions{
		ChatID:                "10001",
		Message:               "<b>Hello</b>",
		ParseMode:             "HTML",
		AttachmentURL:         "https://cdn.example.com/demo.jpg",
		AttachmentDisplayName: "demo.jpg",
	})
	if err != nil {
		t.Fatalf("send with remote photo failed: %v", err)
	}
}
