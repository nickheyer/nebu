package config

import (
	"path/filepath"
	"runtime"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

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
		Gateway: &v1.Gateway{
			DrainTimeoutMs: 30000,
			Policy:         &v1.Policy{UpstreamTimeoutMs: 600000},
			FlowControl: &v1.FlowControl{
				QueueCapacity: 1024, QueueTimeoutMs: 30000, SheddableBelow: -1,
				MetricsStalenessMs: 2000,
			},
			TraceRetention:      "168h",
			TraceRing:           1024,
			DefaultOutputTokens: 256,
		},
		Telemetry: &v1.Telemetry{SampleRatio: 0.1},
		Metrics:   &v1.Metrics{},
		Auth:      &v1.Auth{Oidc: &v1.Oidc{Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups"}, SessionTtl: "24h"},
		Builds:    &v1.Builds{Cli: []string{"podman", "docker", "nerdctl"}, Jobs: uint32(runtime.NumCPU())},
		Web:       &v1.Web{},
		Store:     &v1.Store{},
		Tls:       &v1.Tls{},
		Notify:    &v1.Notify{},
		Discord:   &v1.Discord{},
		Mesh:      &v1.MeshConfig{Announce: proto.Bool(true), Exposure: "guard", Topology: MeshTopologyFull, Sync: MeshSyncPushPull, RoutingMode: RoutingEndpoint},
		Scheduler: &v1.Scheduler{},
	}
}
