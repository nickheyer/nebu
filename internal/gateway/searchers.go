package gateway

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

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/text/language"
)

const (
	searchTimeout = 20 * time.Second
	// Characters Brave takes in one query
	braveQueryMax = 400
)

var braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

// One page a search turned up
type searchHit struct {
	Title, URL, Snippet, Age string
}

// A query and the country its results are localized to
type searchAsk struct {
	Query   string
	Country string
}

// A search that did not happen, with the web_search_tool_result_error code that stands for it
type searchError struct {
	code    string
	message string
}

func (e *searchError) Error() string { return e.message }

// Runs web searches for the web_search tool
type searcher interface {
	search(ctx context.Context, ask searchAsk) ([]searchHit, error)
	// Characters a query may run to, zero for no bound
	queryMax() int
}

// The provider the host settings name
func searcherFor(w *v1.WebSearch, client *http.Client) (searcher, error) {
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

func (s searxng) search(ctx context.Context, ask searchAsk) ([]searchHit, error) {
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
	out := make([]searchHit, 0, len(body.Results))
	for _, r := range body.Results {
		hit := searchHit{Title: r.Title, URL: r.URL, Snippet: r.Content}
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

func (b brave) search(ctx context.Context, ask searchAsk) ([]searchHit, error) {
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
	out := make([]searchHit, 0, len(body.Web.Results))
	for _, r := range body.Web.Results {
		snippet := strings.Join(append([]string{r.Description}, r.ExtraSnippets...), " ")
		age := r.PageAge
		if age == "" {
			age = r.Age
		}
		out = append(out, searchHit{Title: r.Title, URL: r.URL, Snippet: strings.TrimSpace(snippet), Age: age})
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
