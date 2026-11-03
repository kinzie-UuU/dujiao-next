package producthttp

import (
	"regexp"

	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/microcosm-cc/bluemonday"
)

// Catalog HTML can originate with a supplier or a product editor. Apply the
// policy on public reads so it also protects older, already published rows.
var publicContentPolicy = newPublicContentPolicy()

func newPublicContentPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("section", "div", "span", "p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "b", "strong", "i", "em", "u", "s", "del", "code", "pre", "blockquote", "ul", "ol", "li", "table", "thead", "tbody", "tr", "th", "td", "colgroup", "col")
	p.AllowAttrs("data-uuuaigc-service-rules", "data-uuuaigc-membership-rules", "data-content-review", "data-gpt-terms", "data-gpt-warranty", "data-gpt-refund-fee").OnElements("section", "div", "p")
	p.AllowAttrs("start").Matching(regexp.MustCompile(`^-?[0-9]+$`)).OnElements("ol")
	p.AllowAttrs("rowspan", "colspan").Matching(regexp.MustCompile(`^[0-9]+$`)).OnElements("td", "th")
	p.RequireParseableURLs(true).AllowRelativeURLs(true).AllowURLSchemes("https", "http", "mailto", "tel")
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("target").Matching(regexp.MustCompile(`^_blank$`)).OnElements("a")
	p.AllowAttrs("rel").Matching(regexp.MustCompile(`^(?:noopener|noreferrer|nofollow)(?: (?:noopener|noreferrer|nofollow))*$`)).OnElements("a")
	p.AllowAttrs("src").OnElements("img", "video", "source")
	p.AllowAttrs("alt", "title").OnElements("img")
	p.AllowAttrs("controls", "poster").OnElements("video")
	p.AllowAttrs("preload").Matching(regexp.MustCompile(`^(?:none|metadata|auto)$`)).OnElements("video")
	p.AllowStyles("width", "height", "max-width", "max-height").Matching(regexp.MustCompile(`^(?:auto|[0-9]{1,4}(?:px|%))$`)).OnElements("img", "video")
	return p
}

func publicProductContent(content jsonmap.JSON) jsonmap.JSON {
	if len(content) == 0 {
		return content
	}
	out := make(jsonmap.JSON, len(content))
	for locale, value := range content {
		if markup, ok := value.(string); ok {
			out[locale] = publicContentPolicy.Sanitize(markup)
		}
	}
	return out
}
