package mail

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// CIDResolver maps an inline content identifier to an authenticated local URL.
type CIDResolver func(string) string

// SanitizeHTML removes executable/active content and blocks remote image requests.
func SanitizeHTML(input string, resolveCID CIDResolver) (sanitized string, remoteImagesBlocked bool) {
	document, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", false
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "img" {
			for i := range node.Attr {
				if strings.EqualFold(node.Attr[i].Key, "src") {
					source := strings.TrimSpace(node.Attr[i].Val)
					switch {
					case strings.HasPrefix(strings.ToLower(source), "cid:"):
						if local := resolveCID(strings.TrimPrefix(source, "cid:")); local != "" {
							node.Attr[i].Val = local
						} else {
							node.Attr[i].Val = ""
						}
					case isRemoteURL(source):
						remoteImagesBlocked = true
						node.Attr[i].Val = ""
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	var rendered bytes.Buffer
	if err := html.Render(&rendered, document); err != nil {
		return "", remoteImagesBlocked
	}
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "br", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote", "pre", "code", "strong", "b", "em", "i", "u", "s", "hr", "table", "thead", "tbody", "tfoot", "tr", "th", "td", "a", "img")
	policy.AllowAttrs("href", "title").OnElements("a")
	policy.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	policy.AllowAttrs("colspan", "rowspan").OnElements("th", "td")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)
	return policy.Sanitize(rendered.String()), remoteImagesBlocked
}

// TextFromHTML extracts bounded readable text without exposing markup.
func TextFromHTML(input string) string {
	document, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return ""
	}
	var output strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			value := strings.TrimSpace(node.Data)
			if value != "" {
				if output.Len() > 0 {
					output.WriteByte(' ')
				}
				output.WriteString(value)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return strings.Join(strings.Fields(output.String()), " ")
}

func isRemoteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Host != ""
}
