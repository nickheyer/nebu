package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/net/html"
)

const (
	fetchTimeout = 30 * time.Second
	// Bytes read from a page at most
	fetchBodyMax = 8 << 20
	// Characters of a page handed to the model when the call sets no bound
	fetchCharsDefault = 100_000
	// Characters a URL may run to
	fetchURLMax = 2048
)

var fetchSchema = json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The http or https URL of the page to read"}},"required":["url"]}`)

// A fetch server, offering web_fetch
type fetchServer struct {
	name   string
	client *http.Client
}

func newFetch(spec *v1.ToolServer) *fetchServer {
	return &fetchServer{name: spec.GetName(), client: &http.Client{Timeout: fetchTimeout}}
}

func (f *fetchServer) Name() string            { return f.name }
func (f *fetchServer) Kind() v1.ToolServerKind { return v1.ToolServerKind_TOOL_SERVER_KIND_FETCH }

func (f *fetchServer) Tools() []Tool {
	return []Tool{{Name: FetchTool, Inner: FetchTool, ReadOnly: true, Schema: fetchSchema, Description: "Reads a web page and answers with its title and text. Use it on URLs from search results or the conversation"}}
}

// Reads one page under the call's limits, a code naming why it could not
func (f *fetchServer) Call(ctx context.Context, c Call) (*Result, error) {
	var in struct {
		URL string `json:"url"`
	}
	json.Unmarshal(c.Args, &in)
	raw := strings.TrimSpace(in.URL)
	u, err := url.Parse(raw)
	switch {
	case raw == "" || err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https":
		return fetchFailure("invalid_input"), nil
	case len(raw) > fetchURLMax:
		return fetchFailure("url_too_long"), nil
	case len(c.Allowed) > 0 && !underDomains(raw, c.Allowed) || len(c.Blocked) > 0 && underDomains(raw, c.Blocked):
		return fetchFailure("url_not_allowed"), nil
	case c.Exhausted:
		return fetchFailure("max_uses_exceeded"), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fetchFailure("invalid_input"), nil
	}
	req.Header.Set("Accept", "text/html, text/plain, application/json, text/*;q=0.9, */*;q=0.1")
	req.Header.Set("User-Agent", "nebu web_fetch")
	resp, err := f.client.Do(req)
	if err != nil {
		out := fetchFailure("url_not_accessible")
		out.Asked = true
		return out, fmt.Errorf("web_fetch %s: %w", raw, err)
	}
	defer resp.Body.Close()
	asked := func(code string) *Result {
		out := fetchFailure(code)
		out.Asked = true
		return out
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return asked("too_many_requests"), fmt.Errorf("web_fetch %s: rate limited", raw)
	case resp.StatusCode >= http.StatusMultipleChoices:
		return asked("url_not_accessible"), fmt.Errorf("web_fetch %s: answered %d", raw, resp.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchBodyMax))
	if err != nil {
		return asked("url_not_accessible"), fmt.Errorf("web_fetch %s: %w", raw, err)
	}
	page := &Page{URL: resp.Request.URL.String(), MediaType: mediaType, RetrievedAt: time.Now().UTC()}
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "" && looksHTML(body):
		page.Title, page.Text = htmlText(body)
		page.MediaType = "text/plain"
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") || mediaType == "" && utf8.Valid(body):
		page.Text = string(body)
		if page.MediaType == "" {
			page.MediaType = "text/plain"
		}
	default:
		return asked("unsupported_content_type"), nil
	}
	limit := c.MaxChars
	if limit <= 0 {
		limit = fetchCharsDefault
	}
	page.Text = cutRunes(strings.TrimSpace(page.Text), limit)
	return &Result{Parts: []Part{{Type: "text", Text: FetchText(page, "")}}, Page: page, Asked: true}, nil
}

// The result of a fetch that did not happen
func fetchFailure(code string) *Result {
	return &Result{Parts: []Part{{Type: "text", Text: FetchText(nil, code)}}, Failed: true, Code: code}
}

// A fetched page as a model reads it, or what kept the fetch from happening
func FetchText(page *Page, code string) string {
	switch code {
	case "":
	case "max_uses_exceeded":
		return "No more pages may be fetched in this request. Answer with what you have."
	case "invalid_input":
		return "A web_fetch call needs an http or https URL."
	case "url_too_long":
		return "The URL is too long to fetch."
	case "url_not_allowed":
		return "That URL is outside the domains this request may read."
	case "url_not_accessible":
		return "The page could not be fetched."
	case "too_many_requests":
		return "The site is rate limiting. Try again later."
	case "unsupported_content_type":
		return "The page is not text, so it cannot be read."
	default:
		return "Web fetch is unavailable right now."
	}
	var b strings.Builder
	if page.Title != "" {
		b.WriteString(page.Title)
		b.WriteString("\n")
	}
	b.WriteString(page.URL)
	b.WriteString("\n\n")
	b.WriteString(page.Text)
	return b.String()
}

// Whether bytes without a declared type open like an HTML document
func looksHTML(body []byte) bool {
	head := strings.ToLower(string(body[:min(len(body), 512)]))
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html") || strings.Contains(head, "<body")
}

// Elements whose text is not page text
var skipElements = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "head": true, "iframe": true}

// Elements that start a new line in the text
var blockElements = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "section": true, "article": true, "header": true, "footer": true, "nav": true, "aside": true, "blockquote": true, "pre": true, "table": true, "ul": true, "ol": true, "dd": true, "dt": true, "hr": true, "figure": true, "figcaption": true, "main": true, "form": true, "td": true, "th": true}

// The title and visible text of an HTML document, blocks on their own lines
func htmlText(body []byte) (string, string) {
	z := html.NewTokenizer(strings.NewReader(string(body)))
	var text, title strings.Builder
	skip := 0
	inTitle := false
	newline := func() {
		s := text.String()
		if len(s) > 0 && !strings.HasSuffix(s, "\n") {
			text.WriteString("\n")
		}
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(title.String()), squeeze(text.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if tag == "title" {
				inTitle = true
				continue
			}
			if skipElements[tag] {
				skip++
				continue
			}
			if blockElements[tag] {
				newline()
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if tag == "title" {
				inTitle = false
				continue
			}
			if skipElements[tag] && skip > 0 {
				skip--
				continue
			}
			if blockElements[tag] {
				newline()
			}
		case html.TextToken:
			if inTitle {
				title.Write(z.Text())
				continue
			}
			if skip > 0 {
				continue
			}
			text.WriteString(string(z.Text()))
		}
	}
}

// Collapses runs of blank lines and spaces
func squeeze(s string) string {
	var out []string
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Cuts text to a number of runes, marking the cut
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "\n[cut after " + fmt.Sprint(n) + " characters]"
}
