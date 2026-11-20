package sources

import (
	"context"
	"fmt"
	"net/http"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/oauth2"
)

// Models OpenRouter routes to, a key as a bearer token, listed with context_length and a next link:
// https://openrouter.ai/docs/api/api-reference/models/get-models. Or a key OpenRouter issues through its sign-in, no
// callback named so the page shows the code to paste back: https://openrouter.ai/docs/guides/overview/auth/oauth
var openrouter = modelCatalog(v1.SourceKind_SOURCE_KIND_OPENROUTER, "openrouter", "OpenRouter", "https://openrouter.ai/api/v1", v1.ApiFlavor_API_FLAVOR_OPENAI, &SignIn{
	Header: "Authorization",
	Scheme: "Bearer",
	Key:    true,
	OAuth:  &OAuth{Config: oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://openrouter.ai/auth"}}, Keyed: openrouterKey},
})

func init() { register(openrouter) }

// Trades the code the sign-in showed for the key it issued
func openrouterKey(ctx context.Context, client *http.Client, code, verifier string) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	status, err := postJSON(ctx, client, "https://openrouter.ai/api/v1/auth/keys", map[string]string{"code": code, "code_verifier": verifier, "code_challenge_method": "S256"}, &out)
	switch {
	case err != nil:
		return "", err
	case status != http.StatusOK || out.Key == "":
		return "", fmt.Errorf("the code was not taken (%d), sign in again", status)
	}
	return out.Key, nil
}
