package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// Anthropic's Claude models over the Messages API, a key as a bearer token with the API version beside it:
// https://platform.claude.com/docs/en/api/overview, listed as https://platform.claude.com/docs/en/api/models/list answers.
// 2023-06-01 still takes the sampling fields the gateway's Anthropic codec writes, https://platform.claude.com/docs/en/api/versioning
var anthropic = modelCatalog(v1.SourceKind_SOURCE_KIND_ANTHROPIC, "anthropic", "Anthropic", "https://api.anthropic.com/v1", v1.ApiFlavor_API_FLAVOR_ANTHROPIC,
	&SignIn{Header: "Authorization", Scheme: "Bearer", Key: true, Fixed: map[string]string{"anthropic-version": "2023-06-01"}})

func init() { register(anthropic) }
