package runtimes

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// An NVIDIA GPU a seat holds, with the driver index ggml and CUDA name it by
func gpu(id, index string) *v1.Device {
	return &v1.Device{Id: id, Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"index": index}}
}

// A language model descriptor with a layer count
func layered(layers float64) *v1.Descriptor {
	return &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Params: map[string]float64{"n_layer": layers, "n_embd": 4096}}
}

// An install record with facts
func recorded(facts map[string]string) *v1.Install {
	return &v1.Install{Id: "in", Path: "/bin/x", Facts: facts}
}
