package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxAttempts = 4
	baseBackoff = 500 * time.Millisecond
)

// HTTP client with auth and retry for catalog APIs
type Client struct {
	http  *http.Client
	base  *url.URL
	token string
}

// Builds a client for a base endpoint
func NewClient(endpoint, token string) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return nil, err
	}
	return &Client{http: &http.Client{Timeout: 5 * time.Minute}, base: base, token: token}, nil
}

// Joins escaped path segments onto the base URL
func (c *Client) URL(segments ...string) string {
	parts := make([]string, 0, len(segments))
	for _, s := range segments {
		for _, piece := range strings.Split(s, "/") {
			parts = append(parts, url.PathEscape(piece))
		}
	}
	return c.base.String() + "/" + strings.Join(parts, "/")
}

// Sends a request, retrying transient failures on idempotent methods
func (c *Client) Do(ctx context.Context, method, rawURL string, query url.Values, header http.Header) (*http.Response, error) {
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + query.Encode()
	}
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(baseBackoff << (attempt - 1)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		for k, vs := range header {
			req.Header[k] = vs
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			last = err
			if method != http.MethodGet && method != http.MethodHead {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			last = fmt.Errorf("%s %s: %s", method, rawURL, resp.Status)
			resp.Body.Close()
			continue
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("%s %s: %s: %s", method, rawURL, resp.Status, strings.TrimSpace(string(body)))
		}
		return resp, nil
	}
	return nil, last
}

// Fetches JSON into out and returns response headers
func (c *Client) JSON(ctx context.Context, rawURL string, query url.Values, out any) (http.Header, error) {
	resp, err := c.Do(ctx, http.MethodGet, rawURL, query, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return resp.Header, nil
}

// Blob served through HTTP range requests
type RangeBlob struct {
	client *Client
	url    string
	size   int64
	mu     sync.Mutex
}

// Wraps a URL as a range readable blob of known size
func NewRangeBlob(c *Client, rawURL string, size int64) *RangeBlob {
	return &RangeBlob{client: c, url: rawURL, size: size}
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
	header := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", off, end)}}
	b.mu.Lock()
	rawURL := b.url
	b.mu.Unlock()
	resp, err := b.client.Do(context.Background(), http.MethodGet, rawURL, nil, header)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if final := resp.Request.URL.String(); final != rawURL {
		b.mu.Lock()
		b.url = final
		b.mu.Unlock()
	}
	body := io.Reader(resp.Body)
	if resp.StatusCode == http.StatusOK {
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

// Returns blob size
func (b *RangeBlob) Size() int64 { return b.size }

// Releases nothing, ranges are stateless
func (b *RangeBlob) Close() error { return nil }
