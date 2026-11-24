package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/gateway"
)

const (
	// Bytes of a request body the relay reads, the gateway's own bound
	relayMaxBody = 64 << 20
	// Bytes of an answer read before passing them on
	relayChunk = 32 << 10
)

// Headers that belong to one connection and never cross the relay
var relayHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true, "Proxy-Connection": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true, "Content-Length": true, "Host": true,
}

// Request headers a provider never gets beside those: the page a browser sent the request from and the gateway's trace context
var relayInside = map[string]bool{"Origin": true, "Referer": true, "Traceparent": true, "Tracestate": true}

// Whether a request header stays this side of the relay, the gateway's and the mesh's own and a browser's among them
func relayHolds(name string) bool {
	return relayHop[name] || relayInside[name] || strings.HasPrefix(name, "Sec-") || strings.HasPrefix(name, "X-Gateway-") || strings.HasPrefix(name, "X-Nebu-")
}

// Serves one provider's model, forwarding each request to its endpoint with the model renamed and the gateway's headers but those that stay inside
func runRelay(ctx context.Context, e *env, args []string) error {
	fs := e.flags("relay")
	listen := fs.String("listen", "127.0.0.1:0", "address to serve on")
	upstream := fs.String("upstream", "", "the provider's endpoint with its version path, such as https://api.openai.com/v1")
	model := fs.String("model", "", "the id the provider serves the model under")
	served := fs.String("served", "", "the name the relay answers to, the model id when empty")
	positional, err := splitFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 0 || *upstream == "" || *model == "" {
		return errors.New("usage: nebu relay --listen host:port --upstream URL --model ID [--served NAME]")
	}
	base, err := url.Parse(strings.TrimRight(*upstream, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return fmt.Errorf("--upstream %q is not an http or https URL", *upstream)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(e.errw, nil))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	rs := &relayServer{upstream: base, model: *model, served: *served, client: &http.Client{Transport: transport}, log: log}
	if rs.served == "" {
		rs.served = rs.model
	}
	srv := &http.Server{Handler: rs, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("relay listening", "addr", ln.Addr().String(), "upstream", base.String(), "model", rs.model, "served", rs.served)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Forwards to one provider's endpoint, holding no credential of its own
type relayServer struct {
	upstream      *url.URL
	model, served string
	client        *http.Client
	log           *slog.Logger
}

func (s *relayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		io.WriteString(w, "ok\n")
		return
	case "/v1/models":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{{"id": s.served, "object": "model", "owned_by": "nebu"}}})
		return
	}
	// A provider serves its API under the version path alone, so nothing else is forwarded
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		s.fail(w, http.StatusNotFound, fmt.Errorf("%s is not under /v1", r.URL.Path))
		return
	}
	start := time.Now()
	status, err := s.forward(w, r)
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", status, "took", time.Since(start).Round(time.Millisecond)}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	s.log.Info("relayed", attrs...)
}

// Sends the request on and streams the answer back as it arrives, an upstream 401 answered as 502
func (s *relayServer) forward(w http.ResponseWriter, r *http.Request) (int, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, relayMaxBody+1))
	if err != nil {
		return s.fail(w, http.StatusBadRequest, err)
	}
	if len(body) > relayMaxBody {
		return s.fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("request body is over %d bytes", relayMaxBody))
	}
	body = s.renamed(body)
	target := *s.upstream
	target.Path = s.upstream.Path + strings.TrimPrefix(r.URL.Path, "/v1")
	target.RawQuery = r.URL.RawQuery
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return s.fail(w, http.StatusBadGateway, err)
	}
	for k, v := range r.Header {
		if !relayHolds(k) {
			out.Header[k] = v
		}
	}
	resp, err := s.client.Do(out)
	if err != nil {
		return s.fail(w, http.StatusBadGateway, err)
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if !relayHop[k] {
			w.Header()[k] = v
		}
	}
	status := resp.StatusCode
	// The provider refused the credential, which the client must not read as its own nebu token refused, so the gateway alone is told
	if status == http.StatusUnauthorized {
		status = http.StatusBadGateway
		w.Header().Set(gateway.ProviderStatusHeader, strconv.Itoa(resp.StatusCode))
	}
	w.WriteHeader(status)
	flush := http.NewResponseController(w)
	buf := make([]byte, relayChunk)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return status, werr
			}
			flush.Flush()
		}
		if errors.Is(err, io.EOF) {
			return status, nil
		}
		if err != nil {
			return status, err
		}
	}
}

// The body with its top-level model set to the provider's id, unchanged when it is no JSON object naming one
func (s *relayServer) renamed(body []byte) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	if _, ok := doc["model"]; !ok {
		return body
	}
	doc["model"], _ = json.Marshal(s.model)
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// Answers a failure the relay itself met in the shape providers answer theirs
func (s *relayServer) fail(w http.ResponseWriter, status int, err error) (int, error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "relay_error", "message": "relay: " + err.Error()}})
	return status, err
}
