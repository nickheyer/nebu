package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/text/language"
)

const (
	searchTimeout = 20 * time.Second
	// Results handed to the model per search
	searchHits = 10
	// Characters Brave takes in one query
	braveQueryMax = 400
	// Bytes read from a provider's answer at most
	maxBody = 16 << 20
)

var braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

var searchSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"}},"required":["query"]}`)

// A query and the country its results are localized to
type searchAsk struct {
	Query   string
	Country string
}

// A search that did not happen, with the code that stands for it
type searchError struct {
	code    string
	message string
}

func (e *searchError) Error() string { return e.message }

// Runs web searches
type searcher interface {
	search(ctx context.Context, ask searchAsk) ([]Hit, error)
	// Characters a query may run to, zero for no bound
	queryMax() int
}

// A search server, offering web_search through SearXNG or Brave
type searchServer struct {
	name     string
	provider searcher
}

func newSearch(spec *v1.ToolServer) (*searchServer, error) {
	provider, err := searcherFor(spec, &http.Client{Timeout: searchTimeout})
	if err != nil {
		return nil, err
	}
	return &searchServer{name: spec.GetName(), provider: provider}, nil
}

func (s *searchServer) Name() string            { return s.name }
func (s *searchServer) Kind() v1.ToolServerKind { return v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH }

func (s *searchServer) Tools() []Tool {
	return []Tool{{Name: SearchTool, Inner: SearchTool, ReadOnly: true, Schema: searchSchema, Description: "Searches the web and answers with numbered results carrying title, URL, snippet, and date. Search again with another query to find more"}}
}

// Runs one search under the call's limits, a code naming why it did not happen
func (s *searchServer) Call(ctx context.Context, c Call) (*Result, error) {
	var in struct {
		Query string `json:"query"`
	}
	json.Unmarshal(c.Args, &in)
	query := strings.TrimSpace(in.Query)
	switch {
	case query == "":
		return searchFailure("invalid_input"), nil
	case c.Exhausted:
		return searchFailure("max_uses_exceeded"), nil
	case s.provider.queryMax() > 0 && utf8.RuneCountInString(query) > s.provider.queryMax():
		return searchFailure("query_too_long"), nil
	}
	hits, err := s.provider.search(ctx, searchAsk{Query: query, Country: c.Country})
	if err != nil {
		code := "unavailable"
		if failure, ok := errors.AsType[*searchError](err); ok {
			code = failure.code
		}
		out := searchFailure(code)
		out.Asked = true
		return out, fmt.Errorf("web_search %q: %w", query, err)
	}
	kept := make([]Hit, 0, len(hits))
	for _, h := range hits {
		if len(c.Allowed) > 0 && !underDomains(h.URL, c.Allowed) || len(c.Blocked) > 0 && underDomains(h.URL, c.Blocked) {
			continue
		}
		kept = append(kept, h)
		if len(kept) == searchHits {
			break
		}
	}
	return &Result{Parts: []Part{{Type: "text", Text: SearchText(kept, "")}}, Hits: kept, Asked: true}, nil
}

// The result of a search that did not happen
func searchFailure(code string) *Result {
	return &Result{Parts: []Part{{Type: "text", Text: SearchText(nil, code)}}, Failed: true, Code: code}
}

// Search results as a model reads them, or what kept the search from happening
func SearchText(hits []Hit, code string) string {
	switch code {
	case "":
	case "max_uses_exceeded":
		return "No more web searches are allowed in this request. Answer with what you have."
	case "invalid_input":
		return "A web_search call needs a query string."
	case "query_too_long":
		return "The query is too long for the search provider. Shorten it."
	case "too_many_requests":
		return "The search provider is rate limiting. Try again later."
	default:
		return "Web search is unavailable right now."
	}
	if len(hits) == 0 {
		return "No results."
	}
	var b strings.Builder
	b.WriteString("Search results:\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, h.Title, h.URL)
		if h.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", h.Snippet)
		}
		if h.Age != "" {
			fmt.Fprintf(&b, "   published %s\n", h.Age)
		}
	}
	return b.String()
}

// The provider a server's settings name
func searcherFor(w *v1.ToolServer, client *http.Client) (searcher, error) {
	switch w.GetProvider() {
	case "searxng":
		base, err := url.Parse(w.GetSearxngUrl())
		if err != nil || base.Host == "" {
			return nil, fmt.Errorf("the SearXNG URL %q cannot be parsed", w.GetSearxngUrl())
		}
		return searxng{base: base, client: client}, nil
	case "brave":
		if w.GetBraveKey() == "" {
			return nil, errors.New("Brave Search has no subscription token")
		}
		return brave{key: w.GetBraveKey(), client: client}, nil
	}
	return nil, fmt.Errorf("web search provider %q is not searxng or brave", w.GetProvider())
}

// Reads a provider's answer, naming the provider in every failure
func fetchJSON(client *http.Client, req *http.Request, who string, into any) (int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, &searchError{code: "unavailable", message: who + ": " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, &searchError{code: "unavailable", message: who + ": " + err.Error()}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp.StatusCode, &searchError{code: "too_many_requests", message: who + " is rate limiting"}
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return resp.StatusCode, &searchError{code: "unavailable", message: who + " answered in a shape the gateway could not read: " + err.Error()}
	}
	return resp.StatusCode, nil
}

// A SearXNG instance, asked through its JSON format
type searxng struct {
	base   *url.URL
	client *http.Client
}

func (s searxng) queryMax() int { return 0 }

func (s searxng) search(ctx context.Context, ask searchAsk) ([]Hit, error) {
	who := "SearXNG at " + s.base.Host
	q := url.Values{"q": {ask.Query}, "format": {"json"}}
	if locale := searxngLocale(ask.Country); locale != "" {
		q.Set("language", locale)
	}
	u := *s.base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/search"
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &searchError{code: "unavailable", message: who + ": " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	var body struct {
		Results []struct {
			URL           string          `json:"url"`
			Title         string          `json:"title"`
			Content       string          `json:"content"`
			PublishedDate json.RawMessage `json:"publishedDate"`
		} `json:"results"`
	}
	status, err := fetchJSON(s.client, req, who, &body)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusForbidden:
		return nil, &searchError{code: "unavailable", message: who + " refuses JSON answers, add json to search.formats in its settings.yml"}
	case status >= http.StatusMultipleChoices:
		return nil, &searchError{code: "unavailable", message: fmt.Sprintf("%s answered %d", who, status)}
	}
	out := make([]Hit, 0, len(body.Results))
	for _, r := range body.Results {
		hit := Hit{Title: r.Title, URL: r.URL, Snippet: r.Content}
		json.Unmarshal(r.PublishedDate, &hit.Age)
		out = append(out, hit)
	}
	return out, nil
}

// The locale a country most likely searches in, such as en-US for US, or empty when none is known
func searxngLocale(country string) string {
	if country == "" {
		return ""
	}
	tag, err := language.Parse("und-" + country)
	if err != nil {
		return ""
	}
	base, sure := tag.Base()
	region, known := tag.Region()
	if sure == language.No || known == language.No || !region.IsCountry() {
		return ""
	}
	return base.String() + "-" + region.String()
}

// The Brave Search API under a subscription token
type brave struct {
	key    string
	client *http.Client
}

func (b brave) queryMax() int { return braveQueryMax }

// Countries Brave localizes results to
var braveCountries = map[string]bool{
	"AR": true, "AU": true, "AT": true, "BE": true, "BR": true, "CA": true, "CL": true, "DK": true, "FI": true, "FR": true,
	"DE": true, "HK": true, "IN": true, "ID": true, "IT": true, "JP": true, "KR": true, "MY": true, "MX": true, "NL": true,
	"NZ": true, "NO": true, "CN": true, "PL": true, "PT": true, "PH": true, "RU": true, "SA": true, "ZA": true, "ES": true,
	"SE": true, "CH": true, "TW": true, "TR": true, "GB": true, "US": true,
}

func (b brave) search(ctx context.Context, ask searchAsk) ([]Hit, error) {
	who := "Brave Search"
	q := url.Values{"q": {ask.Query}, "count": {"20"}}
	if braveCountries[strings.ToUpper(ask.Country)] {
		q.Set("country", strings.ToUpper(ask.Country))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, &searchError{code: "unavailable", message: who + ": " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.key)
	var body struct {
		Web struct {
			Results []struct {
				Title         string   `json:"title"`
				URL           string   `json:"url"`
				Description   string   `json:"description"`
				PageAge       string   `json:"page_age"`
				Age           string   `json:"age"`
				ExtraSnippets []string `json:"extra_snippets"`
			} `json:"results"`
		} `json:"web"`
	}
	status, err := fetchJSON(b.client, req, who, &body)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return nil, &searchError{code: "unavailable", message: who + " refuses the subscription token"}
	case status >= http.StatusMultipleChoices:
		return nil, &searchError{code: "unavailable", message: fmt.Sprintf("%s answered %d", who, status)}
	}
	out := make([]Hit, 0, len(body.Web.Results))
	for _, r := range body.Web.Results {
		snippet := strings.Join(append([]string{r.Description}, r.ExtraSnippets...), " ")
		age := r.PageAge
		if age == "" {
			age = r.Age
		}
		out = append(out, Hit{Title: r.Title, URL: r.URL, Snippet: strings.TrimSpace(snippet), Age: age})
	}
	return out, nil
}

// Whether a page sits under one of the domains, which cover their subdomains and subpaths
func underDomains(page string, domains []string) bool {
	u, err := url.Parse(page)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	path := strings.TrimSuffix(u.Path, "/") + "/"
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		want, under, _ := strings.Cut(d, "/")
		if want == "" || host != want && !strings.HasSuffix(host, "."+want) {
			continue
		}
		if under != "" && !strings.HasPrefix(path, "/"+strings.TrimSuffix(under, "/")+"/") {
			continue
		}
		return true
	}
	return false
}
