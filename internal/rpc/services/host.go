package services

import (
	"context"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/doctor"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.HostServiceHandler = (*HostService)(nil)

// Serves host profile and doctor
type HostService struct {
	prober *host.Prober
	doctor *doctor.Doctor
}

// Builds the host service
func NewHostService(prober *host.Prober, doc *doctor.Doctor) *HostService {
	return &HostService{prober: prober, doctor: doc}
}

func (s *HostService) GetProfile(ctx context.Context, req *connect.Request[v1.GetProfileRequest]) (*connect.Response[v1.GetProfileResponse], error) {
	profile, err := s.prober.Profile(ctx, req.Msg.GetRefresh())
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.GetProfileResponse{Profile: profile}), nil
}

func (s *HostService) Doctor(ctx context.Context, req *connect.Request[v1.DoctorRequest]) (*connect.Response[v1.DoctorResponse], error) {
	report, err := s.doctor.Run(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	return connect.NewResponse(&v1.DoctorResponse{Report: report}), nil
}
