package pools

import (
	"maps"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// The capabilities the names declare, or the scrape table's for the runtime when no name is given
func capabilitiesOf(names *v1.MetricNames, runtime string) datalayer.MetricCapabilities {
	if proto.Size(names) == 0 {
		caps, _ := scrape.Capabilities(runtime)
		return caps
	}
	return datalayer.MetricCapabilities{
		QueuedRequests:          names.GetQueuedRequests(),
		RunningRequests:         names.GetRunningRequests(),
		KVCacheUtilization:      names.GetKvCacheUtilization(),
		QueuedRequestsMatch:     matcher(names.GetQueuedRequestsMatch()),
		RunningRequestsMatch:    matcher(names.GetRunningRequestsMatch()),
		KVCacheUtilizationMatch: matcher(names.GetKvCacheUtilizationMatch()),
		ReplicaLabel:            names.GetReplicaLabel(),
		CacheConfigInfo:         names.GetCacheConfigInfo(),
		BlockSizeLabel:          names.GetBlockSizeLabel(),
		NumBlocksLabel:          names.GetNumBlocksLabel(),
		BlockSize:               names.GetBlockSize(),
		BlockSizeMatch:          matcher(names.GetBlockSizeMatch()),
		NumBlocks:               names.GetNumBlocks(),
		NumBlocksMatch:          matcher(names.GetNumBlocksMatch()),
		LoRAInfo:                names.GetLoraInfo(),
		MaxLoRALabel:            names.GetMaxLoraLabel(),
		RunningAdaptersLabel:    names.GetRunningAdaptersLabel(),
		WaitingAdaptersLabel:    names.GetWaitingAdaptersLabel(),
		AdaptersPath:            names.GetAdaptersPath(),
		AdaptersFormat:          names.GetAdaptersFormat(),
		MaxLoRAPath:             names.GetMaxLoraPath(),
		MaxLoRAField:            names.GetMaxLoraField(),
		SlotsPath:               names.GetSlotsPath(),
		StaticCache:             names.GetStaticCache(),
		Path:                    names.GetMetricsPath(),
		MetricsPort:             int(names.GetMetricsPort()),
		HealthPath:              names.GetHealthPath(),
	}
}

// A private copy of a label matcher, nil when it names no label
func matcher(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return maps.Clone(m)
}

// The metric names a pool spec shows for its capabilities, nil when it declares none
func namesOf(c datalayer.MetricCapabilities) *v1.MetricNames {
	n := &v1.MetricNames{
		QueuedRequests:          c.QueuedRequests,
		RunningRequests:         c.RunningRequests,
		KvCacheUtilization:      c.KVCacheUtilization,
		QueuedRequestsMatch:     matcher(c.QueuedRequestsMatch),
		RunningRequestsMatch:    matcher(c.RunningRequestsMatch),
		KvCacheUtilizationMatch: matcher(c.KVCacheUtilizationMatch),
		ReplicaLabel:            c.ReplicaLabel,
		CacheConfigInfo:         c.CacheConfigInfo,
		BlockSizeLabel:          c.BlockSizeLabel,
		NumBlocksLabel:          c.NumBlocksLabel,
		BlockSize:               c.BlockSize,
		BlockSizeMatch:          matcher(c.BlockSizeMatch),
		NumBlocks:               c.NumBlocks,
		NumBlocksMatch:          matcher(c.NumBlocksMatch),
		LoraInfo:                c.LoRAInfo,
		MaxLoraLabel:            c.MaxLoRALabel,
		RunningAdaptersLabel:    c.RunningAdaptersLabel,
		WaitingAdaptersLabel:    c.WaitingAdaptersLabel,
		AdaptersPath:            c.AdaptersPath,
		AdaptersFormat:          c.AdaptersFormat,
		MaxLoraPath:             c.MaxLoRAPath,
		MaxLoraField:            c.MaxLoRAField,
		MetricsPath:             c.Path,
		MetricsPort:             uint32(max(c.MetricsPort, 0)),
		HealthPath:              c.HealthPath,
		SlotsPath:               c.SlotsPath,
		StaticCache:             c.StaticCache,
	}
	if proto.Size(n) == 0 {
		return nil
	}
	return n
}

func protoProtocol(p datalayer.AppProtocol) v1.AppProtocol {
	switch p {
	case datalayer.AppProtocolH2C:
		return v1.AppProtocol_APP_PROTOCOL_H2C
	case datalayer.AppProtocolGRPC:
		return v1.AppProtocol_APP_PROTOCOL_GRPC
	case datalayer.AppProtocolHTTP:
		return v1.AppProtocol_APP_PROTOCOL_HTTP
	}
	return v1.AppProtocol_APP_PROTOCOL_UNSPECIFIED
}

// The protocol a spec names as the data layer spells it, HTTP when unspecified
func protocolOf(p v1.AppProtocol) datalayer.AppProtocol {
	switch p {
	case v1.AppProtocol_APP_PROTOCOL_H2C:
		return datalayer.AppProtocolH2C
	case v1.AppProtocol_APP_PROTOCOL_GRPC:
		return datalayer.AppProtocolGRPC
	}
	return datalayer.AppProtocolHTTP
}

func failureModeOf(f v1.FailureMode) datalayer.FailureMode {
	if f == v1.FailureMode_FAILURE_MODE_FAIL_OPEN {
		return datalayer.FailOpen
	}
	return datalayer.FailClose
}

// The data layer pool an effective spec describes
func datalayerPool(id datalayer.PoolID, spec *v1.PoolSpec, caps datalayer.MetricCapabilities) datalayer.Pool {
	ports := make([]int, len(spec.GetTargetPorts()))
	for i, p := range spec.GetTargetPorts() {
		ports[i] = int(p)
	}
	return datalayer.Pool{
		ID:              id,
		Model:           spec.GetModel(),
		Runtime:         spec.GetRuntimeId(),
		Accelerator:     spec.GetAccelerator(),
		Selector:        spec.GetSelector(),
		TargetPorts:     ports,
		AppProtocol:     protocolOf(spec.GetAppProtocol()),
		FailureMode:     failureModeOf(spec.GetFailureMode()),
		SchedulerConfig: spec.GetSchedulerConfig(),
		TokensInOut:     spec.GetTokensInOut(),
		Metrics:         caps,
	}
}
