package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// Any server speaking the OpenAI API, local or remote, config naming its endpoint with the version path
var compatible = func() *Catalog {
	cat := modelCatalog(v1.SourceKind_SOURCE_KIND_OPENAI_COMPATIBLE, "openai-compatible", "OpenAI compatible", "", v1.ApiFlavor_API_FLAVOR_OPENAI, bearerKey())
	cat.Transports[0].Required = []string{"endpoint"}
	cat.Configured = true
	cat.Description = "Any server speaking the OpenAI API, such as https://host/v1, run through the relay with your own key"
	return cat
}()

func init() { register(compatible) }
