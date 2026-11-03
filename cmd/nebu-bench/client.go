package main

import (
	"bytes"
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// Where the gateway and its API answer and how to authenticate to them
type conn struct {
	gatewayURL string
	apiURL     string
	token      string
	tokenFile  string
	tlsCA      string
}

func (c *conn) apiFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.apiURL, "api-url", "", "nebu API base URL, such as https://127.0.0.1:8080")
	c.authFlags(fs)
}

func (c *conn) gatewayFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.gatewayURL, "gateway-url", "", "gateway base URL requests are sent to")
	fs.StringVar(&c.apiURL, "api-url", "", "nebu API base URL, the gateway URL when empty")
	c.authFlags(fs)
}

func (c *conn) authFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.token, "token", "", "bearer token sent to the gateway and the API")
	fs.StringVar(&c.tokenFile, "token-file", "", "file holding the bearer token, such as the daemon's api.token")
	fs.StringVar(&c.tlsCA, "tls-ca", "", "PEM certificates to trust, the daemon's tls.crt or the CA that signed it")
}

// Reads the token file and fills the API URL from the gateway URL
func (c *conn) resolve() error {
	if c.tokenFile != "" {
		if c.token != "" {
			return errors.New("give token or token-file, not both")
		}
		b, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return err
		}
		c.token = strings.TrimSpace(string(b))
	}
	c.apiURL = cmp.Or(c.apiURL, c.gatewayURL)
	return nil
}

// A Connect client for the gateway service sending the bearer token
func (c *conn) api() (nebuv1connect.GatewayServiceClient, error) {
	if c.apiURL == "" {
		return nil, errors.New("the API URL is missing")
	}
	hc, err := c.httpClient(c.apiURL, true, 0)
	if err != nil {
		return nil, err
	}
	hc.Transport = bearer{base: hc.Transport, token: c.token}
	return nebuv1connect.NewGatewayServiceClient(hc, strings.TrimRight(c.apiURL, "/")), nil
}

// Adds the bearer token to every request
type bearer struct {
	base  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.token == "" {
		return b.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

// A client for one base URL keeping conns idle connections, trusting the CA file when one is given
func (c *conn) httpClient(base string, h2 bool, conns int) (*http.Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http or https URL with a host", base)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = h2
	if conns > 0 {
		t.MaxIdleConns, t.MaxIdleConnsPerHost = conns, conns
	}
	if c.tlsCA != "" {
		cfg, err := trusting(c.tlsCA, u.Hostname())
		if err != nil {
			return nil, err
		}
		t.TLSClientConfig = cfg
	}
	return &http.Client{Transport: t}, nil
}

// TLS settings accepting a server whose leaf is one of the file's certificates or chains to one of them
func trusting(path, host string) (*tls.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	var certs []*x509.Certificate
	for block, rest := pem.Decode(data); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		roots.AddCert(cert)
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification moves to VerifyConnection so a self signed leaf without the dialed IP still matches
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: the server presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			for _, c := range certs {
				if bytes.Equal(leaf.Raw, c.Raw) {
					return nil
				}
			}
			opts := x509.VerifyOptions{Roots: roots, DNSName: cmp.Or(cs.ServerName, host), Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := leaf.Verify(opts)
			return err
		},
	}, nil
}
