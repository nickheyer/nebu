package auth

import (
	"fmt"
	"slices"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Provider of the daemon's own auth.token, which names no account
const ProviderDaemon = "daemon"

// Who sent a request and what the credential it carried opens
type Identity struct {
	// Account id or single sign-on subject, empty for the daemon token
	Subject string
	// local, oidc, or daemon
	Provider string
	// Username or the provider's display name, and the email when known
	Name, Email string
	Role        v1.Role
	// What the credential opens, sorted, never beyond the role's own scopes
	Scopes []v1.Scope
	// The API token's prefix, empty for a session or the daemon token
	TokenPrefix string
}

// Whether the credential opens calls needing the scope, admin opening every scope and operate opening read
func (id Identity) Allows(need v1.Scope) bool {
	return slices.ContainsFunc(id.Scopes, func(have v1.Scope) bool { return implies(have, need) })
}

// Whether the identity is an admin holding the admin scope, as account changes on others need
func (id Identity) Admin() bool {
	return id.Role == v1.Role_ROLE_ADMIN && id.Allows(v1.Scope_SCOPE_ADMIN)
}

// The subject the credential's traces and jobs are kept under, the account, else the token prefix, else the provider
func (id Identity) Owner() string {
	for _, v := range []string{id.Subject, id.TokenPrefix, id.Provider} {
		if v != "" {
			return v
		}
	}
	return ""
}

// Whether a held scope opens calls needing another
func implies(have, need v1.Scope) bool {
	switch have {
	case need, v1.Scope_SCOPE_ADMIN:
		return need != v1.Scope_SCOPE_UNSPECIFIED
	case v1.Scope_SCOPE_OPERATE:
		return need == v1.Scope_SCOPE_READ
	}
	return false
}

// The scopes a role grants in enum order, none for an unknown role
func RoleScopes(r v1.Role) []v1.Scope {
	switch r {
	case v1.Role_ROLE_ADMIN:
		return []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ, v1.Scope_SCOPE_OPERATE, v1.Scope_SCOPE_ADMIN}
	case v1.Role_ROLE_OPERATOR:
		return []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ, v1.Scope_SCOPE_OPERATE}
	case v1.Role_ROLE_INFERENCE:
		return []v1.Scope{v1.Scope_SCOPE_INFERENCE}
	}
	return nil
}

// Keeps the scopes the role grants, sorted and without repeats
func capScopes(scopes []v1.Scope, r v1.Role) []v1.Scope {
	granted := RoleScopes(r)
	out := make([]v1.Scope, 0, len(scopes))
	for _, s := range scopes {
		if slices.Contains(granted, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Lowercase short name of a scope or role, such as inference or operator
func shortName(full, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(full, prefix))
}

// Reads a role from its short name, refusing anything but admin, operator, and inference
func ParseRole(name string) (v1.Role, error) {
	r := v1.Role(v1.Role_value["ROLE_"+strings.ToUpper(strings.TrimSpace(name))])
	if r == v1.Role_ROLE_UNSPECIFIED {
		return r, fmt.Errorf("%w: role %q is not admin, operator, or inference", ErrUser, name)
	}
	return r, nil
}

// Short name of a role for logs and the session view
func RoleName(r v1.Role) string { return shortName(r.String(), "ROLE_") }

// Short name of a scope for errors
func ScopeName(s v1.Scope) string { return shortName(s.String(), "SCOPE_") }
