package sources

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Transport IDs used by providers and config fields.
const (
	TransportHTTP         = "http"
	TransportDistribution = "distribution"
	TransportFile         = "file"
	TransportGit          = "git"
	TransportHFCLI        = "hfcli"
)

// Transport for artifact listings and reads. Locators use protocol-specific forms: HTTP URLs,
// registry repo:ref or repo@digest, filesystem paths, or repo@ref/path for git and hf. Unsupported
// listing returns ErrUnsupported.
type Transport interface {
	// Lists artifacts with available sizes and digests.
	List(ctx context.Context, locator string) ([]*v1.Artifact, error)
	// Opens ranged reads. Size is zero if unknown.
	Open(ctx context.Context, locator string, size int64) (Blob, error)
	// Reads one small object whole, up to max bytes
	Read(ctx context.Context, locator string, max int64) ([]byte, error)
}

// Provider transport and exposed settings. Primary settings have bare names. Named transports
// prefix fields, such as registry_endpoint. Inherit reuses selected primary settings.
type Use struct {
	Kind string
	Name string
	// Each offered setting with its default, or for a secret the environment variable seeding it
	Fields   map[string]string
	Required []string
	Inherit  []string
}

// Prefix of the use's field names
func (u Use) prefix() string {
	if u.Name == "" {
		return ""
	}
	return u.Name + "_"
}

// Lookup key, using the kind for the primary transport.
func (u Use) key() string {
	if u.Name == "" {
		return u.Kind
	}
	return u.Name
}

type fieldDef struct {
	name, label, description string
	typ                      v1.ConfigType
}

// The settings each transport kind reads, in display order
var transportFields = map[string][]fieldDef{
	TransportHTTP: {
		{"endpoint", "Endpoint", "API host the source talks to", v1.ConfigType_CONFIG_TYPE_URL},
		{"token", "Token", "", v1.ConfigType_CONFIG_TYPE_SECRET},
		{"username", "Username", "", v1.ConfigType_CONFIG_TYPE_SECRET},
	},
	TransportDistribution: {
		{"endpoint", "Registry", "OCI distribution registry layers are pulled from", v1.ConfigType_CONFIG_TYPE_URL},
		{"token", "Registry credential", "user:secret for the registry's token service", v1.ConfigType_CONFIG_TYPE_SECRET},
	},
	TransportFile: {
		{"path", "Directory", "Directory on this host", v1.ConfigType_CONFIG_TYPE_PATH},
	},
	TransportGit: {
		{"endpoint", "Git host", "Base URL repositories are cloned under", v1.ConfigType_CONFIG_TYPE_URL},
		{"token", "Token", "", v1.ConfigType_CONFIG_TYPE_SECRET},
		{"username", "Username", "", v1.ConfigType_CONFIG_TYPE_SECRET},
	},
	TransportHFCLI: {
		{"command", "CLI command", "hf, huggingface-cli, or an executable path. Empty uses HTTP", v1.ConfigType_CONFIG_TYPE_STRING},
	},
}

// Resolved transport configuration.
type transportEnv struct {
	cacheDir string
}

// Builds a transport from resolved settings.
var transportConstructors = map[string]func(cfg map[string]string, env transportEnv) (Transport, error){
	TransportHTTP:         newHTTPTransport,
	TransportDistribution: newDistributionTransport,
	TransportFile:         newFileTransport,
	TransportGit:          newGitTransport,
	TransportHFCLI:        newHFCLITransport,
}

// Describes the settings a use offers as fields with full names
func (u Use) fields() []*v1.ConfigField {
	var out []*v1.ConfigField
	for _, def := range transportFields[u.Kind] {
		value, offered := u.Fields[def.name]
		if !offered {
			continue
		}
		f := &v1.ConfigField{
			Name:        u.prefix() + def.name,
			Label:       def.label,
			Type:        def.typ,
			Required:    slices.Contains(u.Required, def.name),
			Description: def.description,
			Transport:   u.key(),
		}
		if def.typ == v1.ConfigType_CONFIG_TYPE_SECRET {
			f.Env = value
		} else {
			f.Default = value
		}
		out = append(out, f)
	}
	return out
}

// Checks one value against its field, returning it trimmed
func checkField(f *v1.ConfigField, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if f.GetRequired() {
			return "", fmt.Errorf("%s is required", f.GetName())
		}
		return "", nil
	}
	if len(f.GetChoices()) > 0 && !slices.Contains(f.GetChoices(), value) {
		return "", fmt.Errorf("%s: %q is not one of %s", f.GetName(), value, strings.Join(f.GetChoices(), ", "))
	}
	switch f.GetType() {
	case v1.ConfigType_CONFIG_TYPE_URL:
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || (u.Host == "" && u.Path == "") {
			return "", fmt.Errorf("%s: %q is not a URL with a scheme and host", f.GetName(), value)
		}
		value = strings.TrimRight(value, "/")
	case v1.ConfigType_CONFIG_TYPE_BOOL:
		if _, err := strconv.ParseBool(value); err != nil {
			return "", fmt.Errorf("%s: %q is not true or false", f.GetName(), value)
		}
	case v1.ConfigType_CONFIG_TYPE_INT:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return "", fmt.Errorf("%s: %q is not a whole number", f.GetName(), value)
		}
	case v1.ConfigType_CONFIG_TYPE_PATH:
		abs, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("%s: %v", f.GetName(), err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return "", fmt.Errorf("%s: %q is not a directory", f.GetName(), value)
		}
		value = abs
	}
	return value, nil
}

// Applies source overrides to provider defaults, seeds empty secrets from their variables, and validates fields
func resolveConfig(fields []*v1.ConfigField, values map[string]string) (map[string]string, error) {
	known := map[string]*v1.ConfigField{}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		known[f.GetName()] = f
		out[f.GetName()] = f.GetDefault()
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown setting %q, accepts %s", name, strings.Join(fieldNames(fields), ", "))
		}
		if strings.TrimSpace(values[name]) != "" {
			out[name] = values[name]
		}
	}
	for _, f := range fields {
		if out[f.GetName()] == "" && f.GetEnv() != "" {
			out[f.GetName()] = os.Getenv(f.GetEnv())
		}
		v, err := checkField(f, out[f.GetName()])
		if err != nil {
			return nil, err
		}
		out[f.GetName()] = v
	}
	return out, nil
}

func fieldNames(fields []*v1.ConfigField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.GetName())
	}
	return out
}
