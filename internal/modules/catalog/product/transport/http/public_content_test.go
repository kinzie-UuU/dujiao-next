package producthttp

import (
	"strings"
	"testing"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
)

func TestPublicProductResponseStripsActiveMarkupFromStoredLocales(t *testing.T) {
	content := jsonmap.JSON{
		"zh-CN": `<section data-uuuaigc-service-rules="20260718"><p>说明</p><a href="/faq">帮助</a></section><img src="/uploads/i" style="width:100%;height:auto" onerror="alert(1)">`,
		"zh-TW": `<a href="jav&#x61;script:alert(1)">連結</a><video controls preload="metadata" src="/uploads/v" style="width:100%;max-width:960px"></video><svg><animate onbegin="alert(1)"></animate></svg>`,
		"en-US": `<h3>Usage</h3><strong>Sign in</strong><iframe srcdoc="bad"></iframe><script>alert(1)</script><img src="data:text/html,alert(1)">`,
	}
	view := publicProductView{Product: productdomain.Product{ContentJSON: content}}
	got := view.toProductResp().Content
	for locale, required := range map[string][]string{
		"zh-CN": {`data-uuuaigc-service-rules="20260718"`, "说明", `href="/faq"`, `src="/uploads/i"`, "100%", "auto"},
		"zh-TW": {"連結", "<video", "controls", `src="/uploads/v"`, "960px"},
		"en-US": {"<h3>Usage</h3>", "<strong>Sign in</strong>"},
	} {
		html := got[locale].(string)
		for _, bad := range []string{"alert", "onerror", "onbegin", "javascript:", "<svg", "<iframe", "srcdoc", "<script", "data:text/html"} {
			if strings.Contains(html, bad) {
				t.Errorf("%s retained %q: %s", locale, bad, html)
			}
		}
		for _, term := range required {
			if !strings.Contains(html, term) {
				t.Errorf("%s lost %q: %s", locale, term, html)
			}
		}
	}
	if !strings.Contains(content["zh-CN"].(string), "onerror") {
		t.Error("public read mutated stored content")
	}
}
