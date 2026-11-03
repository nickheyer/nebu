package health

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Service names the gRPC health service always answers for
const (
	ServiceLiveness  = "liveness"
	ServiceReadiness = "readiness"
)

// Which subsystems the gRPC health service reports and how often it reprobes
type GRPCConfig struct {
	// Check names reported as their own services, serving only while their check passes
	Subsystems []string
	// Time between readiness probes, also the deadline of each probe
	Interval time.Duration
}

// A gRPC health service whose statuses follow the registry's checks
type GRPC struct {
	reg *Registry
	cfg GRPCConfig
	srv *grpchealth.Server
}

// Builds the service with liveness serving and readiness and subsystems not serving until probed
func NewGRPC(reg *Registry, cfg GRPCConfig) (*GRPC, error) {
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("health: interval must be positive, got %v", cfg.Interval)
	}
	seen := map[string]bool{ServiceLiveness: true, ServiceReadiness: true}
	for _, name := range cfg.Subsystems {
		if name == "" || seen[name] {
			return nil, fmt.Errorf("health: subsystem name %q is empty, reserved or repeated", name)
		}
		seen[name] = true
	}
	g := &GRPC{reg: reg, cfg: cfg, srv: grpchealth.NewServer()}
	g.srv.SetServingStatus(ServiceLiveness, healthpb.HealthCheckResponse_SERVING)
	g.srv.SetServingStatus(ServiceReadiness, healthpb.HealthCheckResponse_NOT_SERVING)
	for _, name := range cfg.Subsystems {
		g.srv.SetServingStatus(name, healthpb.HealthCheckResponse_NOT_SERVING)
	}
	return g, nil
}

// The underlying health server for mounting or direct calls
func (g *GRPC) Server() healthpb.HealthServer { return g.srv }

// Probes readiness once and sets readiness and every subsystem from the result
func (g *GRPC) Refresh(ctx context.Context) Report {
	rep := g.reg.Readiness(ctx)
	g.srv.SetServingStatus(ServiceReadiness, servingStatus(rep.OK))
	for _, name := range g.cfg.Subsystems {
		res, ok := rep.Result(name)
		g.srv.SetServingStatus(name, servingStatus(ok && res.OK))
	}
	return rep
}

// Refreshes every interval until ctx ends, then turns every service to not serving
func (g *GRPC) Run(ctx context.Context) {
	t := time.NewTicker(g.cfg.Interval)
	defer t.Stop()
	defer g.srv.Shutdown()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, g.cfg.Interval)
			g.Refresh(pctx)
			cancel()
		}
	}
}

// Mounts a health service on s, probes once, and keeps it refreshed until ctx ends
func Serve(ctx context.Context, s grpc.ServiceRegistrar, reg *Registry, cfg GRPCConfig) (*GRPC, error) {
	g, err := NewGRPC(reg, cfg)
	if err != nil {
		return nil, err
	}
	healthpb.RegisterHealthServer(s, g.srv)
	pctx, cancel := context.WithTimeout(ctx, cfg.Interval)
	g.Refresh(pctx)
	cancel()
	go g.Run(ctx)
	return g, nil
}

func servingStatus(ok bool) healthpb.HealthCheckResponse_ServingStatus {
	if ok {
		return healthpb.HealthCheckResponse_SERVING
	}
	return healthpb.HealthCheckResponse_NOT_SERVING
}
