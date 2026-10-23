// Package config loads daemon and client configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"sigs.k8s.io/yaml"
)

const (
	// API token the daemon generates in data_dir when auth.token is empty
	TokenFile = "api.token"
	// Environment variables NEBU_ plus the upper case key path, dots as underscores, override every key
	envPrefix = "NEBU"
)

var (
	// Config file names tried in each search directory
	fileNames = []string{"config.yaml", "config.yml"}
	// The system wide config directory, searched before the user's
	systemDir = filepath.Join(string(filepath.Separator)+"etc", "nebu")
)

// Loads the file at path, or the first config.yaml on the search path, then the NEBU_ environment
func Load(path string) (*v1.Config, error) {
	dataDir, err := dataHome()
	if err != nil {
		return nil, err
	}
	cacheDir, _ := os.UserCacheDir()
	cfg := defaults(dataDir, cacheDir)
	file, err := locate(path)
	if err != nil {
		return nil, err
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		tree, err := parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := apply(cfg.ProtoReflect(), tree, ""); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	if err := applyEnv(cfg.ProtoReflect(), envPrefix, environ()); err != nil {
		return nil, err
	}
	derive(cfg)
	return cfg, nil
}

// Every setting's value before the file and environment
func defaults(dataDir, cacheDir string) *v1.Config {
	return &v1.Config{
		Listen:       "127.0.0.1:8484",
		DataDir:      filepath.Join(dataDir, "nebu"),
		CacheDir:     filepath.Join(cacheDir, "nebu"),
		Logging:      &v1.Logging{Level: "info", Format: "text"},
		Contexts:     []uint32{8192, 32768, 131072},
		MinFreeBytes: 50 << 30,
		Transfer:     &v1.Transfer{Workers: 8, ChunkBytes: 32 << 20, Retries: 5},
		Gateway:      &v1.Gateway{DrainTimeoutMs: 30000, Policy: &v1.Policy{UpstreamTimeoutMs: 600000}},
		Auth:         &v1.Auth{Oidc: &v1.Oidc{Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups"}, SessionTtl: "24h"},
		Builds:       &v1.Builds{Cli: []string{"podman", "docker", "nerdctl"}, Jobs: uint32(runtime.NumCPU())},
		Web:          &v1.Web{},
		Store:        &v1.Store{},
		Tls:          &v1.Tls{},
		Notify:       &v1.Notify{},
		Discord:      &v1.Discord{},
		Mesh:         &v1.MeshConfig{Announce: proto.Bool(true), Exposure: "guard"},
	}
}

// Fills keys whose defaults come from keys the file or environment may have set
func derive(cfg *v1.Config) {
	if cfg.StoreDir == "" {
		cfg.StoreDir = filepath.Join(cfg.GetDataDir(), "store")
	}
	if cfg.Builds.Dir == "" {
		cfg.Builds.Dir = filepath.Join(cfg.GetDataDir(), "builds")
	}
	if cfg.Auth.Oidc.Name == "" {
		cfg.Auth.Oidc.Name = issuerHost(cfg.GetAuth().GetOidc().GetIssuer())
	}
}

// The given file, or the first config file under /etc/nebu then $HOME/.config/nebu, empty when there is none
func locate(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	dirs := []string{systemDir}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "nebu"))
	}
	for _, dir := range dirs {
		for _, name := range fileNames {
			file := filepath.Join(dir, name)
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				return file, nil
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
	}
	return "", nil
}

// Reads YAML into a key tree, numbers kept as written
func parse(data []byte) (map[string]any, error) {
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return nil, errors.New("the file must hold a mapping of keys to values")
	}
	return tree, nil
}

// Sets the message's fields from a key tree, refusing keys the message does not have
func apply(m protoreflect.Message, tree map[string]any, prefix string) error {
	fields := m.Descriptor().Fields()
	for key, value := range tree {
		fd := fields.ByName(protoreflect.Name(key))
		if fd == nil {
			fd = fields.ByJSONName(key)
		}
		if fd == nil {
			return fmt.Errorf("unknown key %s", prefix+key)
		}
		if err := set(m, fd, value, prefix+key); err != nil {
			return err
		}
	}
	return nil
}

// Sets one field from a file value or an environment string
func set(m protoreflect.Message, fd protoreflect.FieldDescriptor, value any, key string) error {
	if value == nil {
		m.Clear(fd)
		return nil
	}
	switch {
	case fd.IsMap():
		tree, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected a mapping", key)
		}
		mp := m.Mutable(fd).Map()
		for k, v := range tree {
			val, err := scalar(fd.MapValue(), v, key+"."+k)
			if err != nil {
				return err
			}
			mp.Set(protoreflect.ValueOfString(k).MapKey(), val)
		}
		return nil
	case fd.IsList():
		list := m.Mutable(fd).List()
		list.Truncate(0)
		items, err := listOf(value, fd, key)
		if err != nil {
			return err
		}
		for i, item := range items {
			at := fmt.Sprintf("%s[%d]", key, i)
			if fd.Kind() == protoreflect.MessageKind {
				tree, ok := item.(map[string]any)
				if !ok {
					return fmt.Errorf("%s: expected a mapping", at)
				}
				if err := apply(list.AppendMutable().Message(), tree, at+"."); err != nil {
					return err
				}
				continue
			}
			val, err := scalar(fd, item, at)
			if err != nil {
				return err
			}
			list.Append(val)
		}
		return nil
	case fd.Kind() == protoreflect.MessageKind:
		tree, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected a mapping", key)
		}
		return apply(m.Mutable(fd).Message(), tree, key+".")
	}
	val, err := scalar(fd, value, key)
	if err != nil {
		return err
	}
	m.Set(fd, val)
	return nil
}

// The items of a list value, a string from the environment split on commas
func listOf(value any, fd protoreflect.FieldDescriptor, key string) ([]any, error) {
	switch v := value.(type) {
	case []any:
		return v, nil
	case string:
		if fd.Kind() == protoreflect.MessageKind {
			return nil, fmt.Errorf("%s: expected a list of mappings", key)
		}
		var out []any
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: expected a list", key)
}

// One scalar as the field's kind, strings parsed for numbers, booleans, and enum names
func scalar(fd protoreflect.FieldDescriptor, value any, key string) (protoreflect.Value, error) {
	text := ""
	switch v := value.(type) {
	case string:
		text = v
	case json.Number:
		text = v.String()
	case bool:
		text = strconv.FormatBool(v)
	case float64:
		text = strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return protoreflect.Value{}, fmt.Errorf("%s: expected a single value", key)
	}
	wrong := func(what string) (protoreflect.Value, error) {
		return protoreflect.Value{}, fmt.Errorf("%s: %q is not %s", key, text, what)
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(text), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(strings.TrimSpace(text))
		if err != nil {
			return wrong("true or false")
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
		if err != nil {
			return wrong("a whole number")
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return wrong("a whole number")
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(strings.TrimSpace(text), 10, 32)
		if err != nil {
			return wrong("a whole number of zero or more")
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return wrong("a whole number of zero or more")
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(strings.TrimSpace(text), 32)
		if err != nil {
			return wrong("a number")
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return wrong("a number")
		}
		return protoreflect.ValueOfFloat64(f), nil
	case protoreflect.EnumKind:
		return enumValue(fd.Enum(), text, key)
	}
	return protoreflect.Value{}, fmt.Errorf("%s: %s values cannot be set from a file", key, fd.Kind())
}

// An enum value by its number or by name, with or without the type prefix, in any case
func enumValue(ed protoreflect.EnumDescriptor, text, key string) (protoreflect.Value, error) {
	name := strings.TrimSpace(text)
	if n, err := strconv.ParseInt(name, 10, 32); err == nil {
		if v := ed.Values().ByNumber(protoreflect.EnumNumber(n)); v != nil {
			return protoreflect.ValueOfEnum(v.Number()), nil
		}
	}
	values := ed.Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if full := string(v.Name()); strings.EqualFold(full, name) || strings.HasSuffix(full, "_"+strings.ToUpper(name)) {
			return protoreflect.ValueOfEnum(v.Number()), nil
		}
	}
	return protoreflect.Value{}, fmt.Errorf("%s: %q is not a %s", key, text, ed.Name())
}

// The environment variables under the prefix, set and non-empty ones only
func environ() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, envPrefix+"_") && value != "" {
			out[name] = value
		}
	}
	return out
}

// Sets every field a variable names, nested messages under their own prefix
func applyEnv(m protoreflect.Message, prefix string, env map[string]string) error {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := prefix + "_" + strings.ToUpper(string(fd.Name()))
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
			if !hasPrefix(env, name+"_") {
				continue
			}
			if err := applyEnv(m.Mutable(fd).Message(), name, env); err != nil {
				return err
			}
			continue
		}
		value, ok := env[name]
		if !ok {
			continue
		}
		if fd.IsMap() || fd.Kind() == protoreflect.MessageKind {
			return fmt.Errorf("%s: %s holds structured settings, which only the config file can set", name, keyOf(name))
		}
		if err := set(m, fd, value, name); err != nil {
			return err
		}
	}
	return nil
}

func hasPrefix(env map[string]string, prefix string) bool {
	for name := range env {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// The config key an environment variable names
func keyOf(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, envPrefix+"_"), "_", "."))
}

// The issuer host, or the issuer itself when it is not a URL
func issuerHost(issuer string) string {
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Hostname()
	}
	return issuer
}

// Where the platform keeps application data, XDG on unix
func dataHome() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
