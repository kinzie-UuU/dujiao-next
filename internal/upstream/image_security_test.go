package upstream

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
)

func TestDownloadImageRejectsUnsafeRedirectsAndContent(t *testing.T) {
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls.Add(1); _, _ = w.Write([]byte("secret")) }))
	defer foreign.Close()
	var loops atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/foreign":
			http.Redirect(w, r, foreign.URL, http.StatusFound)
		case "/loop":
			loops.Add(1)
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/html.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/huge":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", maxUpstreamImageBytes)))
		default:
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	adapter := NewDujiaoNextAdapter(&siteconnectiondomain.Connection{BaseURL: server.URL}, dir)
	adapter.imageClient = server.Client()
	for _, target := range []string{"/foreign", "/loop", "/html.png", "/huge", strings.Replace(server.URL, "http:", "https:", 1) + "/image"} {
		if _, err := adapter.DownloadImage(context.Background(), target); err == nil {
			t.Errorf("unsafe image accepted: %s", target)
		}
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("followed cross-host redirect")
	}
	if loops.Load() > 10 {
		t.Fatalf("redirect bound exceeded: %d", loops.Load())
	}
	files, err := filepath.Glob(filepath.Join(dir, "upstream", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("rejected response left files: %v %v", files, err)
	}
	local, err := adapter.DownloadImage(context.Background(), "/download.html?name=wrong.jpg")
	if err != nil || !strings.HasSuffix(local, ".png") {
		t.Fatalf("extension must match sniffed image: %q %v", local, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "upstream", filepath.Base(local)))
	if err != nil || string(data) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("image bytes changed: %q %v", data, err)
	}
}

func TestImageTransportRejectsPrivateTargets(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fe80::1", "0.0.0.0", "0.1.2.3", "255.255.255.255", "198.18.0.1", "192.0.2.1", "2001:db8::1"} {
		if isPublicImageIP(net.ParseIP(value)) {
			t.Errorf("private IP allowed: %s", value)
		}
	}
	if !isPublicImageIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
	conn, err := imageDialContext(&net.Dialer{Timeout: time.Second})(context.Background(), "tcp", "127.0.0.1:1")
	if conn != nil {
		conn.Close()
	}
	if err != errImageTargetForbidden {
		t.Fatalf("private dial not rejected before connection: %v", err)
	}
}
