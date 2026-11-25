package smtp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Optional local-only fixtures. No SMTP connection and no real verification code.
func TestExportVerifyCardPreview(t *testing.T) {
	dir := os.Getenv("VERIFY_CARD_PREVIEW_DIR")
	if dir == "" {
		t.Skip("preview export disabled")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, purpose := range []string{"register", "reset", "telegram_bind", "change_email_old", "change_email_new"} {
			card, err := renderVerifyCard("285619", purpose, locale)
			if err != nil {
				t.Fatal(err)
			}
			card = strings.ReplaceAll(card, "cid:uuuaigc-verification-logo", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(verifyCardLogo))
			if err := os.WriteFile(filepath.Join(dir, locale+"-"+purpose+".html"), []byte(card), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
