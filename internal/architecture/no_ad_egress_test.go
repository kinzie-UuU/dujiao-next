package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ads are not part of the storefront's business integrations. Guard both build
// variants, including code that a default (non-release) test build would omit.
func TestBackendHasNoAdvertisingGateway(t *testing.T) {
	root := filepath.Join(findRepositoryRoot(t), "internal")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"ads-gateway.dujiao-next.com", "/ad-events/impression", "/internal/modules/adproxy"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("advertising egress must not be reintroduced: %s contains %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
