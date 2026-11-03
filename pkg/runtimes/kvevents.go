package runtimes

import (
	"encoding/json"
	"net"
	"strconv"
)

// Topic every rank publishes its KV cache events under
const kvEventsTopic = "kv-events"

// A runtime whose launches publish KV cache events the gateway's prefix index subscribes to
type KVEventPublisher interface {
	// Host and port a rank publishes events on and replays missed batches from, empty when the launch publishes none
	KVEvents(params map[string]string, host string, port, rank int) (events, replay string)
}

// Ports one rank binds as recorded, its HTTP port and the gRPC port after it when the launch serves gRPC
func recordedRankSpan(params map[string]string) int {
	if paramOn(params, "grpc") {
		return 2
	}
	return 1
}

// The events and replay ports of a rank, laid out after every rank's serving ports and any extra port
func kvEventPorts(launchPort, ranks, span, extra, rank int) (events, replay int) {
	first := launchPort + ranks*span + extra
	return first + rank, first + ranks + rank
}

// The events and replay addresses of a rank whose HTTP port is port, zero ports giving none
func kvEventAddresses(host string, port, rank, ranks, span, extra int) (string, string) {
	events, replay := kvEventPorts(port-rank*span, ranks, span, extra, rank)
	return net.JoinHostPort(host, strconv.Itoa(events)), net.JoinHostPort(host, strconv.Itoa(replay))
}

// The JSON a ZMQ publisher of batches on the events port with replays from the replay port is configured by
func kvEventsConfig(bind string, events, replay int, fields map[string]any) string {
	cfg := map[string]any{
		"publisher":       "zmq",
		"endpoint":        "tcp://" + net.JoinHostPort(bind, strconv.Itoa(events)),
		"replay_endpoint": "tcp://" + net.JoinHostPort(bind, strconv.Itoa(replay)),
		"topic":           kvEventsTopic,
	}
	for k, v := range fields {
		cfg[k] = v
	}
	data, _ := json.Marshal(cfg)
	return string(data)
}
