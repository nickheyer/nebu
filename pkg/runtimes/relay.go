package runtimes

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats/relay"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Relay serves a provider's models through nebu's own relay, one runtime per API since the gateway picks its codec by runtime id
type Relay struct {
	Speaks v1.ApiFlavor
}

// The relay of each API
func relays() []Runtime {
	return []Runtime{Relay{Speaks: v1.ApiFlavor_API_FLAVOR_OPENAI}, Relay{Speaks: v1.ApiFlavor_API_FLAVOR_ANTHROPIC}}
}

func (r Relay) ID() string { return "relay-" + text.Enum(r.Speaks) }

func (r Relay) Name() string { return "Relay (" + r.apiName() + " API)" }

func (r Relay) Description() string {
	return "Serves a provider's models over the " + r.apiName() + " API, each request carrying the caller's own credential"
}

func (r Relay) apiName() string {
	if r.Speaks == v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		return "Anthropic"
	}
	return "OpenAI"
}

func (r Relay) Formats() []string                { return []string{text.Enum(r.Speaks)} }
func (Relay) Kind() v1.ModelKind                 { return v1.ModelKind_MODEL_KIND_LANGUAGE }
func (r Relay) API() v1.ApiFlavor                { return r.Speaks }
func (Relay) Requirements() []string             { return nil }
func (Relay) Unmet(*v1.HostProfile) []string     { return nil }
func (Relay) Params() []*v1.Param                { return nil }
func (Relay) Prepares(string) bool               { return false }
func (Relay) Prepare(Launch) (*Command, error)   { return nil, nil }
func (Relay) PrepareTimeout() time.Duration      { return 0 }
func (Relay) StopGrace() time.Duration           { return 5 * time.Second }
func (Relay) Policy() *estimate.Policy           { return nil }
func (Relay) Probes() []Probe                    { return nil }
func (Relay) Measure([]string) []*v1.Measurement { return nil }
func (Relay) Triage() []triage.Set               { return nil }

// The nebu binary itself, which the daemon adopts at start
func (Relay) Methods() []Method {
	return []Method{{ID: "adopt", Description: "The nebu binary, adopted by the daemon at start", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"nebu"}}}
}

// Runs nebu relay toward the endpoint and model id the stored reference names
func (r Relay) Launch(in Launch) (*Command, error) {
	meta := in.Descriptor.GetMetadata()
	endpoint, model := meta[relay.KeyEndpoint], meta[relay.KeyModel]
	if endpoint == "" || model == "" {
		return nil, fmt.Errorf("%w: the stored model names no endpoint or model id, pull it again", ErrParam)
	}
	if api := meta[relay.KeyAPI]; api != text.Enum(r.Speaks) {
		return nil, fmt.Errorf("%w: the stored model is served over the %s API, run it on relay-%s", ErrParam, api, api)
	}
	args := []string{"relay", "--listen", net.JoinHostPort(in.Host, strconv.Itoa(in.Port)), "--upstream", endpoint, "--model", model, "--served", in.Name}
	return &Command{Command: in.Install.Path, Args: args, Env: map[string]string{}, Params: map[string]string{}}, nil
}

func (Relay) Health() Health {
	return Health{Path: "/health", Interval: 5 * time.Second, SlowAfter: 30 * time.Second}
}

// One relay per instance, never a seat of a formation
func (Relay) Shapes(*v1.Install) []v1.Shape { return []v1.Shape{v1.Shape_SHAPE_SOLO} }

func (Relay) Roles(shape v1.Shape) []Role {
	if shape == v1.Shape_SHAPE_SOLO {
		return soloRoles()
	}
	return nil
}

func (r Relay) LaunchSeat(in Launch) (*Command, error) {
	if in.Seat == nil {
		return nil, fmt.Errorf("%w: a seat launch needs a seat block", ErrParam)
	}
	if _, err := RoleOf(r, in.Seat); err != nil {
		return nil, err
	}
	return r.Launch(in)
}

func (Relay) Transport([]string) string                           { return "" }
func (Relay) MetricsPort(_ map[string]string, port int) int       { return port }
func (Relay) AppProtocol(map[string]string) v1.AppProtocol        { return v1.AppProtocol_APP_PROTOCOL_HTTP }
func (Relay) GRPCPort(map[string]string, int) int                 { return 0 }
func (Relay) PortSpan(Launch) int                                 { return 1 }
func (Relay) Ranks(map[string]string) int                         { return 1 }
func (Relay) ExpertParallel(map[string]string) int                { return 1 }
func (Relay) Capacity(map[string]string) uint32                   { return 0 }
func (Relay) KVCapacity(map[string]string, *v1.MemoryPlan) uint32 { return 0 }
func (Relay) Adapters(string, map[string]string) AdapterClient    { return nil }
func (Relay) Sleeper(string, map[string]string) SleepClient       { return nil }

// No load gauges, the provider's load being its own, and the health path the gateway's probe reads
func (Relay) Metrics() *v1.MetricNames { return &v1.MetricNames{HealthPath: "/health"} }

// The context the provider reports, text in and out
func (Relay) Inventory(in Launch, _ map[string]string) (*v1.RuntimeInventory, error) {
	return &v1.RuntimeInventory{ContextLength: uint32(trained(in)), Modalities: []string{"text"}}, nil
}

// Go names the address it could not bind
var goBindRe = regexp.MustCompile(`listen tcp [^ ]*:(\d+): bind: address already in use`)

func (Relay) PortRule() triage.Rule {
	return triage.Rule{
		ID:      portRuleID,
		Summary: "the relay found its port taken by another process",
		Hint:    "reserve another port and launch again",
		Match: func(line string) (map[string]string, bool) {
			if m := goBindRe.FindStringSubmatch(line); m != nil {
				return map[string]string{"port": m[1]}, true
			}
			return nil, false
		},
	}
}
