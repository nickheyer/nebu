package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	maxAttempts   = 4
	baseBackoff   = 500 * time.Millisecond
	userAgent     = "nebu (+https://github.com/nickheyer/nebu)"
	dialTimeout   = 30 * time.Second
	headerTimeout = 2 * time.Minute
)

// HTTP transport for one host with auth and retry
//
// Listing answers an S3 style ListObjectsV2 under a prefix, which is what
// buckets and the mirrors exported into them speak. Opening serves range
// reads on a URL.
type HTTP struct {
	http       *http.Client
	base       *url.URL
	token      string
	scheme     string
	authorizer func(ctx context.Context) http.Header
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
	return &HTTP{http: &http.Client{Transport: newTransport()}, base: base, token: token, scheme: "Bearer"}, nil
}

// A transport that bounds the dial, the handshake, and the wait for headers, never the body
//
// A body is read under the transfer limits, which may hold it for as long as a
// window says, so only the steps a stuck server could hang on carry a deadline.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = dialTimeout
	t.ResponseHeaderTimeout = headerTimeout
	return t
}

// Builds the transport, none when the source names no endpoint and the provider requires none
func newHTTPTransport(cfg map[string]string, _ transportEnv) (Transport, error) {
	if cfg["endpoint"] == "" {
		return nil, nil
	}
	h, err := NewHTTP(cfg["endpoint"], envValue(cfg, "token_env"))
	if err != nil {
		return nil, err
	}
	if user := envValue(cfg, "username_env"); user != "" && h.token != "" {
		h.UseBasic(user)
	}
	return h, nil
}

// Sends the token as HTTP basic auth instead of a bearer
func (c *HTTP) UseBasic(username string) {
	c.scheme = "Basic"
	if username != "" && !strings.Contains(c.token, ":") {
		c.token = username + ":" + c.token
	}
}

// Replaces the token with headers computed per request, for keys that are exchanged first
func (c *HTTP) SetAuthorizer(fn func(ctx context.Context) http.Header) {
	c.authorizer = fn
}

// Returns the base endpoint without a trailing slash
func (c *HTTP) Base() string {
	return c.base.String()
}

// Joins escaped path segments onto the base URL
func (c *HTTP) URL(segments ...string) string {
	return c.base.String() + joinPath(segments...)
}

// Turns a locator into a URL, keeping one that is already absolute
func (c *HTTP) Absolute(locator string) string {
	if strings.Contains(locator, "://") {
		return locator
	}
	return c.base.String() + "/" + strings.TrimLeft(locator, "/")
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
	if req.Header.Get("Authorization") != "" {
		return
	}
	if c.authorizer != nil {
		for k, vs := range c.authorizer(req.Context()) {
			req.Header[k] = vs
		}
		return
	}
	if c.token == "" {
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
	data, err := c.read(ctx, rawURL, query, header, max)
	return string(data), err
}

func (c *HTTP) read(ctx context.Context, rawURL string, query url.Values, header http.Header, max int64) ([]byte, error) {
	resp, err := c.Do(ctx, http.MethodGet, rawURL, query, header)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readAllCapped(resp.Body, max)
}

// Reads a stream up to max bytes, everything when max is not positive
func readAllCapped(r io.Reader, max int64) ([]byte, error) {
	if max > 0 {
		r = io.LimitReader(r, max)
	}
	return io.ReadAll(r)
}

// Reads a URL whole, capped
func (c *HTTP) Read(ctx context.Context, locator string, max int64) ([]byte, error) {
	return c.read(ctx, c.Absolute(locator), nil, nil, max)
}

// Opens a URL for range reads, asking the server for the size when the caller has none
func (c *HTTP) Open(ctx context.Context, locator string, size int64) (Blob, error) {
	rawURL := c.Absolute(locator)
	if size <= 0 {
		resp, err := c.Do(ctx, http.MethodHead, rawURL, nil, nil)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		if size = resp.ContentLength; size <= 0 {
			return nil, unknown(rawURL, "size")
		}
	}
	return NewRangeBlob(c, rawURL, size), nil
}

type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key  string `xml:"Key"`
		Size uint64 `xml:"Size"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// Lists objects under a prefix through ListObjectsV2, paths relative to the prefix
func (c *HTTP) List(ctx context.Context, locator string) ([]*v1.Artifact, error) {
	prefix := strings.Trim(locator, "/")
	if prefix != "" {
		prefix += "/"
	}
	var out []*v1.Artifact
	token := ""
	for {
		q := url.Values{"list-type": {"2"}}
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		var res listResult
		if err := c.listPage(ctx, q, &res); err != nil {
			return nil, err
		}
		for _, o := range res.Contents {
			rel := strings.TrimPrefix(o.Key, prefix)
			if rel == "" || strings.HasSuffix(rel, "/") {
				continue
			}
			out = append(out, &v1.Artifact{Path: rel, SizeBytes: o.Size})
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return out, nil
		}
		token = res.NextContinuationToken
	}
}

// Lists the first level of prefixes under a prefix, the directories of a bucket
func (c *HTTP) Prefixes(ctx context.Context, locator string) ([]string, error) {
	prefix := strings.Trim(locator, "/")
	if prefix != "" {
		prefix += "/"
	}
	q := url.Values{"list-type": {"2"}, "delimiter": {"/"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	var res listResult
	if err := c.listPage(ctx, q, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range res.CommonPrefixes {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(p.Prefix, prefix), "/"))
	}
	return out, nil
}

func (c *HTTP) listPage(ctx context.Context, q url.Values, out *listResult) error {
	resp, err := c.Do(ctx, http.MethodGet, c.Base()+"/", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return xml.NewDecoder(resp.Body).Decode(out)
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

// The rel next link of a page's Link header, relative ones under the base
func (c *HTTP) nextLink(h http.Header) string { return NextLink(h.Get("Link"), c.Base()) }

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

// Names a URL and headers to fetch a blob from, called before every range for links that expire
type Resolver func(ctx context.Context) (string, http.Header, error)

// Blob served through HTTP range requests
type RangeBlob struct {
	client   *HTTP
	url      string
	size     int64
	resolver Resolver
	mu       sync.Mutex
}

// Wraps a URL as a range readable blob
func NewRangeBlob(c *HTTP, rawURL string, size int64) *RangeBlob {
	return &RangeBlob{client: c, url: rawURL, size: size}
}

// Asks for fresh headers before every range request, for tokens that expire
func (b *RangeBlob) WithHeaderFunc(fn func(ctx context.Context) (http.Header, error)) *RangeBlob {
	b.resolver = func(ctx context.Context) (string, http.Header, error) {
		h, err := fn(ctx)
		return "", h, err
	}
	return b
}

// Asks for the URL and headers before every range request, for links that expire
func (b *RangeBlob) WithResolver(fn Resolver) *RangeBlob {
	b.resolver = fn
	return b
}

// Picks the URL and headers for one range request
func (b *RangeBlob) prepare(ctx context.Context, rng string) (string, http.Header, error) {
	h := http.Header{"Range": {rng}}
	b.mu.Lock()
	rawURL := b.url
	b.mu.Unlock()
	if b.resolver != nil {
		resolved, extra, err := b.resolver(ctx)
		if err != nil {
			return "", nil, err
		}
		if resolved != "" {
			rawURL = resolved
		}
		for k, vs := range extra {
			h[k] = vs
		}
	}
	return rawURL, h, nil
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
	rawURL, header, err := b.prepare(context.Background(), fmt.Sprintf("bytes=%d-%d", off, end))
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
	rawURL, header, err := b.prepare(ctx, fmt.Sprintf("bytes=%d-%d", off, end))
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
	if final == rawURL || signedURL(resp.Request.URL) || b.resolver != nil {
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
