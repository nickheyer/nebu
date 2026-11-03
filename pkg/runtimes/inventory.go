package runtimes

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Throughput classes an inventory reports, where a launch's weights run
const (
	ThroughputDevice  = "device"
	ThroughputOffload = "offload"
	ThroughputHost    = "host"
)

// The accelerator a launch reports when it runs on no device
const acceleratorCPU = "cpu"

// The one modality a launch that generates no text reports, embedding vectors, rerank or class scores, or other pooled output
const (
	ModalityEmbedding = "embedding"
	ModalityRerank    = "rerank"
	ModalityClassify  = "classify"
	ModalityPooling   = "pooling"
)

// Every modality a launch generating no text reports alone
var PoolingModalities = []string{ModalityEmbedding, ModalityRerank, ModalityClassify, ModalityPooling}

// The serving launch's context, quantization and modalities with the devices of every launch and the slowest class
func SpanInventory(serving *v1.RuntimeInventory, launches []*v1.RuntimeInventory) (*v1.RuntimeInventory, error) {
	if serving == nil || len(launches) == 0 {
		return nil, fmt.Errorf("an endpoint spanning launches needs the serving launch's inventory and every launch's")
	}
	out := proto.Clone(serving).(*v1.RuntimeInventory)
	var names []string
	count := map[string]int{}
	cpu, onHost, onDevice := false, false, false
	for _, inv := range launches {
		for part := range strings.SplitSeq(inv.GetAccelerator(), " + ") {
			if part == acceleratorCPU {
				cpu = true
				continue
			}
			n, name, ok := strings.Cut(part, "x ")
			k, err := strconv.Atoi(n)
			if !ok || err != nil || k <= 0 || name == "" {
				return nil, fmt.Errorf("accelerator %q is not a cpu or count by device list", inv.GetAccelerator())
			}
			if count[name] == 0 {
				names = append(names, name)
			}
			count[name] += k
		}
		switch inv.GetThroughputClass() {
		case ThroughputDevice:
			onDevice = true
		case ThroughputOffload:
			onHost, onDevice = true, true
		case ThroughputHost:
			onHost = true
		default:
			return nil, fmt.Errorf("throughput class %q is not %s, %s or %s", inv.GetThroughputClass(), ThroughputDevice, ThroughputOffload, ThroughputHost)
		}
	}
	parts := make([]string, 0, len(names)+1)
	for _, name := range names {
		parts = append(parts, strconv.Itoa(count[name])+"x "+name)
	}
	if cpu || len(parts) == 0 {
		parts = append(parts, acceleratorCPU)
	}
	out.Accelerator = strings.Join(parts, " + ")
	switch {
	case onHost && !onDevice:
		out.ThroughputClass = ThroughputHost
	case onHost:
		out.ThroughputClass = ThroughputOffload
	default:
		out.ThroughputClass = ThroughputDevice
	}
	return out, nil
}
