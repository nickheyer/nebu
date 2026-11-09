package runtimes

import (
	"regexp"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

// None, sd-server serves no Prometheus endpoint
func (SDCpp) Metrics() *v1.MetricNames { return nil }

func (SDCpp) MetricsPort(_ map[string]string, port int) int { return port }

func (SDCpp) AppProtocol(map[string]string) v1.AppProtocol { return v1.AppProtocol_APP_PROTOCOL_HTTP }

func (SDCpp) GRPCPort(map[string]string, int) int { return 0 }

func (SDCpp) PortSpan(Launch) int { return 1 }

// One rank, a diffusion server running no data parallel ranks
func (SDCpp) Ranks(map[string]string) int { return 1 }

// One, a diffusion model holding no expert layers
func (SDCpp) ExpertParallel(map[string]string) int { return 1 }

// One generation at a time, which sd-server runs to the end before the next
func (SDCpp) Capacity(map[string]string) uint32 { return 1 }

// Zero since a diffusion server holds no KV cache
func (SDCpp) KVCapacity(map[string]string, *v1.MemoryPlan) uint32 { return 0 }

func (SDCpp) Adapters(string, map[string]string) AdapterClient { return nil }

func (SDCpp) Sleeper(string, map[string]string) SleepClient { return nil }

// No context, text in and generated images or video out, offload when parts stream from host memory
func (SDCpp) Inventory(in Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	inv, err := baseInventory(in, params)
	if err != nil {
		return nil, err
	}
	inv.Modalities = append([]string{"text"}, in.Descriptor.GetGenerates()...)
	if params["offload"] == "on" && inv.ThroughputClass == ThroughputDevice {
		inv.ThroughputClass = ThroughputOffload
	}
	return inv, nil
}

// sd-server announces its address before it listens and exits without a word when the listen fails
var sdListenRe = regexp.MustCompile(`listening on: https?://\S*:(\d+)`)

// The listen announcement in the log of a launch that exited before answering health, the bind having failed
func (SDCpp) PortRule() triage.Rule {
	return triage.Rule{
		ID:      portRuleID,
		Summary: "stable-diffusion.cpp exited right after announcing its port, which another process holds",
		Hint:    "reserve another port and launch again",
		Match: func(line string) (map[string]string, bool) {
			if m := sdListenRe.FindStringSubmatch(line); m != nil {
				return map[string]string{"port": m[1]}, true
			}
			return nil, false
		},
	}
}
