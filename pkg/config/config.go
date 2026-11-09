// Package config loads daemon and client configuration
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"buf.build/go/protovalidate"
	"github.com/go-viper/mapstructure/v2"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/spf13/viper"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	// API token the daemon generates in data_dir when auth.token is empty
	TokenFile = "api.token"
	// Self-signed listener pair the daemon generates in data_dir when tls is enabled and names none
	CertFile = "tls.crt"
	KeyFile  = "tls.key"
	// Environment variables NEBU_ plus the upper case key path, dots as underscores, override every key
	envPrefix    = "NEBU"
	keyDelimiter = "::"
)

// Mesh topologies, sync exchanges and import routing modes mesh settings name
const (
	MeshTopologyFull  = "full"
	MeshTopologyHub   = "hub"
	MeshTopologySpoke = "spoke"
	MeshSyncPushPull  = "push-pull"
	MeshSyncPush      = "push"
	MeshSyncPull      = "pull"
	RoutingEndpoint   = "endpoint"
	RoutingParent     = "parent"
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
	v := viper.NewWithOptions(viper.KeyDelimiter(keyDelimiter))
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(keyDelimiter, "_"))
	if err := bindEnv(v, cfg.ProtoReflect().Descriptor(), ""); err != nil {
		return nil, err
	}
	if file != "" {
		v.SetConfigFile(file)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	if err := v.UnmarshalExact(cfg, decoding); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	derive(cfg)
	if err := protovalidate.Validate(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
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
	DeriveTLS(cfg)
}

// Points tls at the pair generated in data_dir when it is enabled and names none
func DeriveTLS(cfg *v1.Config) {
	if cfg.Tls == nil {
		cfg.Tls = &v1.Tls{}
	}
	if cfg.Tls.Enabled && cfg.Tls.CertFile == "" && cfg.Tls.KeyFile == "" {
		cfg.Tls.CertFile, cfg.Tls.KeyFile = filepath.Join(cfg.GetDataDir(), CertFile), filepath.Join(cfg.GetDataDir(), KeyFile)
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

func bindEnv(v *viper.Viper, md protoreflect.MessageDescriptor, prefix string) error {
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		key := prefix + string(fd.Name())
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
			if err := bindEnv(v, fd.Message(), key+keyDelimiter); err != nil {
				return err
			}
			continue
		}
		if err := v.BindEnv(key); err != nil {
			return err
		}
	}
	return nil
}

func decoding(c *mapstructure.DecoderConfig) {
	c.TagName = "json"
	c.DecodeHook = mapstructure.ComposeDecodeHookFunc(decodeEnum, splitList)
}

var enumType = reflect.TypeFor[protoreflect.Enum]()

func decodeEnum(_, to reflect.Type, data any) (any, error) {
	if !to.Implements(enumType) {
		return data, nil
	}
	ed := reflect.Zero(to).Interface().(protoreflect.Enum).Descriptor()
	name := strings.TrimSpace(fmt.Sprint(data))
	for i := range ed.Values().Len() {
		v := ed.Values().Get(i)
		if full := string(v.Name()); strings.EqualFold(full, name) || strings.HasSuffix(full, "_"+strings.ToUpper(name)) || strconv.Itoa(int(v.Number())) == name {
			return int32(v.Number()), nil
		}
	}
	return nil, fmt.Errorf("%q is not a %s", name, ed.Name())
}

func splitList(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to.Kind() != reflect.Slice {
		return data, nil
	}
	items := []string{}
	for _, item := range strings.Split(reflect.ValueOf(data).String(), ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items, nil
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
