package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxAttempts = 4
	baseBackoff = 500 * time.Millisecond
	userAgent   = "nebu (+https://github.com/nickheyer/nebu)"
)

// HTTP client for one host with auth and retry
type HTTP struct {
	http   *http.Client
	base   *url.URL
	token  string
	scheme string
	header http.Header
}

// Builds a client for a base endpoint sending token as a bearer
func NewHTTP(endpoint, token string) (*HTTP, error) {
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return nil, err
	}
	if base.Scheme == "" {
		return nil, fmt.Errorf("endpoint %q needs a scheme", endpoint)
	}
	return &HTTP{http: &http.Client{Timeout: 5 * time.Minute}, base: base, token: token, scheme: "Bearer", header: http.Header{}}, nil
}

// Sends the token as HTTP basic auth instead of a bearer
func (c *HTTP) UseBasic(username string) {
	c.scheme = "Basic"
	if username != "" && !strings.Contains(c.token, ":") {
		c.token = username + ":" + c.token
	}
}

// Adds a header to every request
func (c *HTTP) SetHeader(key, value string) {
	c.header.Set(key, value)
}

// Returns the base endpoint without a trailing slash
func (c *HTTP) Base() string {
	return c.base.String()
}

// Reports whether a token is configured
func (c *HTTP) HasToken() bool {
	return c.token != ""
}

// Joins escaped path segments onto the base URL
func (c *HTTP) URL(segments ...string) string {
	return c.base.String() + joinPath(segments...)
}

// Joins path segments into an escaped path with a leading slash, splitting segments that carry slashes
func joinPath(segments ...string) string {
	var parts []string
	for _, s := range segments {
		for _, piece := range strings.Split(s, "/") {
			if piece != "" {
				parts = append(parts, url.PathEscape(piece))
			}
		}
	}
	return "/" + strings.Join(parts, "/")
}

// Applies the configured authorization to a request when it has none
func (c *HTTP) authorize(req *http.Request) {
	if c.token == "" || req.Header.Get("Authorization") != "" {
		return
	}
	switch c.scheme {
	case "Basic":
		user, pass, _ := strings.Cut(c.token, ":")
		req.SetBasicAuth(user, pass)
	default:
		req.Header.Set("Authorization", c.scheme+" "+c.token)
	}
}

// Sends a request, retrying transient failures on idempotent methods
func (c *HTTP) Do(ctx context.Context, method, rawURL string, query url.Values, header http.Header) (*http.Response, error) {
	return c.DoBody(ctx, method, rawURL, query, header, nil)
}

// Sends a request with a body, retrying transient failures on idempotent methods
func (c *HTTP) DoBody(ctx context.Context, method, rawURL string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + query.Encode()
	}
	idempotent := method == http.MethodGet || method == http.MethodHead
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(baseBackoff << (attempt - 1)):
			}
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		for k, vs := range c.header {
			req.Header[k] = vs
		}
		for k, vs := range header {
			req.Header[k] = vs
		}
		c.authorize(req)
		resp, err := c.http.Do(req)
		if err != nil {
			last = err
			if !idempotent {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			last = fmt.Errorf("%s %s: %s", method, rawURL, resp.Status)
			resp.Body.Close()
			if !idempotent {
				return nil, last
			}
			continue
		}
		if resp.StatusCode >= 400 {
			detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, &StatusError{Method: method, URL: rawURL, Code: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(detail)), Header: resp.Header}
		}
		return resp, nil
	}
	return nil, last
}

// Sends a prepared request once with the client's auth header
func (c *HTTP) Send(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}
	c.authorize(req)
	return c.http.Do(req)
}

// Fetches JSON into out and returns response headers
func (c *HTTP) JSON(ctx context.Context, rawURL string, query url.Values, out any) (http.Header, error) {
	return c.JSONWith(ctx, http.MethodGet, rawURL, query, nil, nil, out)
}

// Sends an optional JSON body and decodes the JSON answer
func (c *HTTP) JSONBody(ctx context.Context, method, rawURL string, query url.Values, body any, out any) (http.Header, error) {
	return c.JSONWith(ctx, method, rawURL, query, nil, body, out)
}

// Sends a request with extra headers and an optional JSON body, decoding the JSON answer
func (c *HTTP) JSONWith(ctx context.Context, method, rawURL string, query url.Values, header http.Header, body any, out any) (http.Header, error) {
	var data []byte
	h := http.Header{"Accept": {"application/json"}}
	for k, vs := range header {
		h[k] = vs
	}
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return nil, err
		}
		h.Set("Content-Type", "application/json")
	}
	resp, err := c.DoBody(ctx, method, rawURL, query, h, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if out == nil {
		return resp.Header, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return resp.Header, nil
}

// Fetches a text body, capped, for cards and scraped pages
func (c *HTTP) Text(ctx context.Context, rawURL string, query url.Values, max int64) (string, error) {
	return c.TextWith(ctx, rawURL, query, nil, max)
}

// Fetches a text body with extra headers, capped
func (c *HTTP) TextWith(ctx context.Context, rawURL string, query url.Values, header http.Header, max int64) (string, error) {
	resp, err := c.Do(ctx, http.MethodGet, rawURL, query, header)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// Resolves the rel next link of a Link header, relative ones against base, empty when there is none
func NextLink(link, base string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		return base + u.String()
	}
	return u.String()
}

// HTTP error with the status and a body excerpt
type StatusError struct {
	Method string
	URL    string
	Code   int
	Status string
	Body   string
	Header http.Header
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.URL, e.Status, e.Body)
}

// Reports whether err is an HTTP error with the given status
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}

// Blob served through HTTP range requests
type RangeBlob struct {
	client  *HTTP
	url     string
	size    int64
	header  http.Header
	headers func(ctx context.Context) (http.Header, error)
	mu      sync.Mutex
}

// Wraps a URL as a range readable blob
func NewRangeBlob(c *HTTP, rawURL string, size int64) *RangeBlob {
	return &RangeBlob{client: c, url: rawURL, size: size}
}

// Adds headers to every range request
func (b *RangeBlob) WithHeader(h http.Header) *RangeBlob {
	b.header = h
	return b
}

// Asks for fresh headers before every range request, for tokens that expire
func (b *RangeBlob) WithHeaderFunc(fn func(ctx context.Context) (http.Header, error)) *RangeBlob {
	b.headers = fn
	return b
}

func (b *RangeBlob) requestHeaders(ctx context.Context, rng string) (http.Header, error) {
	h := http.Header{"Range": {rng}}
	for k, vs := range b.header {
		h[k] = vs
	}
	if b.headers != nil {
		extra, err := b.headers(ctx)
		if err != nil {
			return nil, err
		}
		for k, vs := range extra {
			h[k] = vs
		}
	}
	return h, nil
}

// Reads one range, tolerating servers that ignore Range
func (b *RangeBlob) ReadAt(p []byte, off int64) (int, error) {
	if off >= b.size {
		return 0, io.EOF
	}
	end := off + int64(len(p)) - 1
	if end >= b.size {
		end = b.size - 1
	}
	b.mu.Lock()
	rawURL := b.url
	b.mu.Unlock()
	header, err := b.requestHeaders(context.Background(), fmt.Sprintf("bytes=%d-%d", off, end))
	if err != nil {
		return 0, err
	}
	resp, err := b.client.Do(context.Background(), http.MethodGet, rawURL, nil, header)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b.remember(resp, rawURL)
	body := io.Reader(resp.Body)
	if ignoredRange(resp) {
		if _, err := io.CopyN(io.Discard, body, off); err != nil {
			return 0, err
		}
	}
	want := int(end - off + 1)
	n, err := io.ReadFull(body, p[:want])
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if n == want && err == io.EOF {
		err = nil
	}
	if n < len(p) && err == nil {
		err = io.EOF
	}
	return n, err
}

// Streams one range, tolerating servers that ignore Range
func (b *RangeBlob) Range(ctx context.Context, off, length int64) (io.ReadCloser, error) {
	if off >= b.size || length <= 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	end := min(off+length-1, b.size-1)
	b.mu.Lock()
	rawURL := b.url
	b.mu.Unlock()
	header, err := b.requestHeaders(ctx, fmt.Sprintf("bytes=%d-%d", off, end))
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(ctx, http.MethodGet, rawURL, nil, header)
	if err != nil {
		return nil, err
	}
	b.remember(resp, rawURL)
	var body io.Reader = resp.Body
	if ignoredRange(resp) {
		if _, err := io.CopyN(io.Discard, body, off); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	return &limitedBody{Reader: io.LimitReader(body, end-off+1), closer: resp.Body}, nil
}

// Keeps a redirect target so later ranges skip the hop, unless it is signed and short lived
func (b *RangeBlob) remember(resp *http.Response, rawURL string) {
	final := resp.Request.URL.String()
	if final == rawURL || signedURL(resp.Request.URL) {
		return
	}
	b.mu.Lock()
	b.url = final
	b.mu.Unlock()
}

// Reports whether a URL carries a signature that expires
func signedURL(u *url.URL) bool {
	q := u.Query()
	for _, k := range []string{"X-Amz-Signature", "Expires", "Signature", "X-Goog-Signature", "sig", "token"} {
		if q.Get(k) != "" {
			return true
		}
	}
	return false
}

// Reports a whole body response, some send 200 with Content-Range
func ignoredRange(resp *http.Response) bool {
	return resp.StatusCode == http.StatusOK && !strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes ")
}

type limitedBody struct {
	io.Reader
	closer io.Closer
}

func (l *limitedBody) Close() error { return l.closer.Close() }

// Returns blob size
func (b *RangeBlob) Size() int64 { return b.size }

// Releases nothing, ranges are stateless
func (b *RangeBlob) Close() error { return nil }
