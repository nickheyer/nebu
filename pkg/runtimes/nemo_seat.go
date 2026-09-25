package runtimes

import (
	"fmt"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// NeMo serves each replica alone: its framework server has no rank or handoff protocol nebu drives
func (NeMo) Shapes(*v1.Install) []v1.Shape {
	return []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_REPLICAS}
}

func (NeMo) Roles(shape v1.Shape) []Role {
	switch shape {
	case v1.Shape_SHAPE_REPLICAS:
		return replicaRoles()
	case v1.Shape_SHAPE_SOLO:
		return soloRoles()
	}
	return nil
}

func (NeMo) Transport(lines []string) string { return transportOf(lines) }

func (r NeMo) LaunchSeat(in Launch) (*Command, error) {
	if in.Seat == nil {
		return nil, fmt.Errorf("%w: a seat launch needs a seat block", ErrParam)
	}
	if _, err := RoleOf(r, in.Seat); err != nil {
		return nil, err
	}
	if in.Seat.GetShape() == v1.Shape_SHAPE_REPLICAS {
		return r.Launch(exposedLaunch(in))
	}
	return r.Launch(in)
}
