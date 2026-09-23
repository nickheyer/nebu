// Package sso signs users into the web UI through an OpenID Connect provider.
// A signed-in browser holds the sealed session cookie the API and gateway
// accept in place of the bearer token and API key.
package sso

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	httpTimeout = 15 * time.Second
	// Scope every OpenID Connect request must carry
	openidScope = "openid"
)

// Signs users in through one provider
type Service struct {
	cfg      *v1.Oidc
	scopes   []string
	client   *http.Client
	sessions *auth.Sessions
	log      *slog.Logger
	now      func() time.Time

	mu   sync.Mutex
	prov *provider
}

// Validates the provider settings. Sessions it signs in go through sessions.
func New(cfg *v1.Oidc, sessions *auth.Sessions, log *slog.Logger) (*Service, error) {
	u, err := url.Parse(cfg.GetIssuer())
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("auth.oidc.issuer %q must be an http or https URL", cfg.GetIssuer())
	}
	if cfg.GetClientId() == "" {
		return nil, errors.New("auth.oidc.client_id is required")
	}
	if p := cfg.GetPublicUrl(); p != "" {
		pu, err := url.Parse(p)
		if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
			return nil, fmt.Errorf("auth.oidc.public_url %q must be an http or https origin", p)
		}
	}
	if sessions == nil {
		return nil, errors.New("single sign-on needs sessions")
	}
	if cfg.GetGroupsClaim() == "" {
		return nil, errors.New("auth.oidc.groups_claim is required")
	}
	scopes := slices.Clone(cfg.GetScopes())
	if !slices.Contains(scopes, openidScope) {
		scopes = append([]string{openidScope}, scopes...)
	}
	return &Service{
		cfg:      cfg,
		scopes:   scopes,
		client:   &http.Client{Timeout: httpTimeout},
		sessions: sessions,
		log:      log,
		now:      time.Now,
	}, nil
}

// Provider name for sign-in buttons
func (s *Service) Name() string { return s.cfg.GetName() }

// What the provider said about the user
type identity struct {
	subject string
	email   string
	name    string
	// Nil when the provider did not say
	verified *bool
	groups   []string
}

func (s *Service) identityFrom(claims map[string]any) identity {
	id := identity{subject: claimString(claims, "sub"), email: claimString(claims, "email")}
	if v, ok := claimBool(claims, "email_verified"); ok {
		id.verified = &v
	}
	for _, key := range []string{"name", "preferred_username", "email", "sub"} {
		if v := claimString(claims, key); v != "" {
			id.name = v
			break
		}
	}
	if v, ok := claimLookup(claims, s.cfg.GetGroupsClaim()); ok {
		id.groups = claimStrings(v)
	}
	return id
}

// Whether the ID token left out claims the allow lists need, which userinfo may carry
func (s *Service) needsUserinfo(id identity) bool {
	if id.email == "" {
		return true
	}
	return len(s.cfg.GetAllowedGroups()) > 0 && len(id.groups) == 0
}

// Fills claims the ID token lacks from userinfo
func merge(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range extra {
		out[k] = v
	}
	for k, v := range base {
		out[k] = v
	}
	return out
}

// Applies the allow lists. Emails and domains admit a user when either matches,
// and allowed groups must match as well when set.
func (s *Service) allowed(id identity) error {
	emails, domains, groups := s.cfg.GetAllowedEmails(), s.cfg.GetAllowedDomains(), s.cfg.GetAllowedGroups()
	who := id.email
	if who == "" {
		who = id.subject
	}
	if len(emails) > 0 || len(domains) > 0 {
		if id.email == "" {
			return fmt.Errorf("%s has no email claim to check against the allow lists, add the email scope", who)
		}
		if id.verified != nil && !*id.verified {
			return fmt.Errorf("%s has an unverified email", who)
		}
		domain := id.email[strings.LastIndex(id.email, "@")+1:]
		ok := slices.ContainsFunc(emails, func(e string) bool { return strings.EqualFold(e, id.email) }) ||
			slices.ContainsFunc(domains, func(d string) bool { return strings.EqualFold(strings.TrimPrefix(d, "@"), domain) })
		if !ok {
			return fmt.Errorf("%s is not in auth.oidc.allowed_emails or allowed_domains", who)
		}
	}
	if len(groups) > 0 && !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(id.groups, g) }) {
		return fmt.Errorf("%s is in none of auth.oidc.allowed_groups, the %s claim holds %v", who, s.cfg.GetGroupsClaim(), id.groups)
	}
	return nil
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }
