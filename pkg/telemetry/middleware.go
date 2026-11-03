package telemetry

import (
	"bufio"
	"net"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Set instead of a status code when the handler took over the connection without writing one
const AttrHijacked = attribute.Key("http.connection.hijacked")

// Wraps next in a server span per request, continuing any incoming trace and naming the model from modelHeader
func Middleware(next http.Handler, modelHeader string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := Extract(r.Context(), r.Header)
		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLPath(r.URL.Path),
		}
		if model := r.Header.Get(modelHeader); modelHeader != "" && model != "" {
			attrs = append(attrs, AttrModel.String(model))
		}
		ctx, span := otel.Tracer(scope).Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
		defer span.End()

		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(ctx))

		if sw.hijacked && sw.status == 0 {
			span.SetAttributes(AttrHijacked.Bool(true))
			return
		}
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		span.SetAttributes(semconv.HTTPResponseStatusCode(sw.status))
		if sw.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		}
	})
}

// Records the final status a handler answers while passing flushes and hijacks through
type statusWriter struct {
	http.ResponseWriter
	status   int
	hijacked bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && (code >= http.StatusOK || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flushes the underlying writer, an implicit 200 when no header was written
func (w *statusWriter) FlushError() error {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Flush() { _ = w.FlushError() }

// Hands the connection to the handler when the underlying writer supports it
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.hijacked = true
	}
	return c, rw, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
