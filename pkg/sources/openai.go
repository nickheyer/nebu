package sources

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/oauth2"
)

// OpenAI's models, a key as a bearer token: https://developers.openai.com/api/reference/resources/models/methods/list.
// Or a ChatGPT plan through Sign in with ChatGPT for open-source apps, a client registered on first sign-in and the
// loopback address the browser lands on copied back: https://developers.openai.com/siwc/token-sharing-open-source/sign-in,
// refreshed and revoked as https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions says, with the
// keys, issuer and revocation endpoint of https://auth.openai.com/.well-known/openid-configuration. Plan usage takes the
// Responses API alone, stored false and streamed, system turns as instructions, function tools grouped in a namespace, and
// no max_output_tokens, temperature or top_p: https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations.
// Any other path answers 401 rejected_by_access_enforcement, so the gateway renders every chat under a sign-in for it.
var openai = modelCatalog(v1.SourceKind_SOURCE_KIND_OPENAI, "openai", "OpenAI", "https://api.openai.com/v1", v1.ApiFlavor_API_FLAVOR_OPENAI, &SignIn{
	Header: "Authorization",
	Scheme: "Bearer",
	Key:    true,
	OAuth: &OAuth{
		Config: oauth2.Config{
			ClientID:    "dynamic_agent_client",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://auth.openai.com/api/accounts/authorize", TokenURL: "https://auth.openai.com/api/accounts/oauth/token", AuthStyle: oauth2.AuthStyleInParams},
			RedirectURL: "http://127.0.0.1:1455/auth/callback",
			Scopes:      []string{"openid", "profile", "email", "offline_access", "resource.invoke", "chatgpt.tokens.use.direct"},
		},
		Authorize: map[string]string{"ext_agent_host_id": "{host}", "resource": "https://api.openai.com/v1", "nonce": "{nonce}"},
		Registers: map[string]string{"agent_name_hint": "nebu"},
		Token:     map[string]string{"resource": "https://api.openai.com/v1"},
		Issues:    true,
		Requires:  "chatgpt.tokens.use.direct",
		JWKS:      "https://auth.openai.com/.well-known/jwks.json",
		Issuer:    "https://auth.openai.com",
		Revoke:    "https://auth.openai.com/api/accounts/oauth/revoke",
		Responses: &Responses{Namespace: "functions"},
	},
})

func init() {
	// Resets as durations such as 6m0s, https://developers.openai.com/api/docs/guides/rate-limits
	openai.Limits = &Limits{Header: "x-ratelimit-{field}-{window}", Windows: []string{"requests", "tokens", "project-tokens"}}
	// A stream names its usage only when asked through stream_options,
	// https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create
	openai.StreamUsage = true
	register(openai)
}
