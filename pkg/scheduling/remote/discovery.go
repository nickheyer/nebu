package remote

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// The port a scheduler server listens on when its address names none
	DefaultPort = 8486
	// The host name a scheduler server is resolved by when the configuration names no URL
	ServiceName = "nebu-scheduler"
	// The path the server answers liveness on with GET
	HealthPath = "/healthz"
	// The path the server serves its Prometheus families on with GET
	MetricsPath = "/metrics"
	// The endpoint label carrying the id the endpoint has on the scheduler that exported it
	LabelExportedID = "exported-id"
)

// The URL the service name and default port resolve to
var DefaultURL = "https://" + net.JoinHostPort(ServiceName, strconv.Itoa(DefaultPort))

// The scheduler URL a configuration value resolves to, the service name and default port filling what it leaves out
func ResolveURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(DefaultPort))
	}
	return u.String()
}

// A transport trusting the certificate a PEM file holds exactly and every other server through the system roots
func PinnedTransport(certFile string) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = true
	if certFile == "" {
		return t, nil
	}
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("scheduler certificate: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("scheduler certificate %s holds no PEM block", certFile)
	}
	own, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("scheduler certificate %s: %w", certFile, err)
	}
	t.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the scheduler presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if bytes.Equal(leaf.Raw, own.Raw) {
				return nil
			}
			opts := x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := leaf.Verify(opts)
			return err
		},
	}
	return t, nil
}

// The id a candidate is named by on the wire, the one its exporter gave it when it carries one
func candidateID(ep datalayer.Endpoint) string {
	if id := ep.Labels()[LabelExportedID]; id != "" {
		return id
	}
	return string(ep.ID())
}

// Schedule call counts by outcome and their durations, served on the metrics path
type families struct {
	registry *prometheus.Registry
	calls    *prometheus.CounterVec
	duration prometheus.Histogram
}

func newFamilies() *families {
	f := &families{
		registry: prometheus.NewRegistry(),
		calls:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nebu_scheduler_schedule_total", Help: "Schedule calls answered by outcome"}, []string{"outcome"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "nebu_scheduler_schedule_duration_seconds", Help: "Time one schedule call took", Buckets: prometheus.ExponentialBuckets(0.0005, 2, 14)}),
	}
	f.registry.MustRegister(f.calls, f.duration)
	return f
}

// The handler serving the families in the Prometheus text format
func (f *families) handler() http.Handler {
	return promhttp.HandlerFor(f.registry, promhttp.HandlerOpts{})
}
