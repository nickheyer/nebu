package installs

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/conditions"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Condition types and reasons an install reports
const (
	ConditionAccepted = conditions.Accepted
	ConditionReady    = conditions.Ready

	ReasonAccepted       = "Accepted"
	ReasonUnknownRuntime = "UnknownRuntime"
	ReasonRelativePath   = "RelativePath"
	ReasonBinaryPresent  = "BinaryPresent"
	ReasonBinaryMissing  = "BinaryMissing"
	ReasonNotExecutable  = "NotExecutable"
)

// Sets the install's generation from the stored record and its status from what the host shows now
func (m *Manager) observe(ctx context.Context, in *v1.Install) error {
	prev, err := m.DB.GetInstall(ctx, in.GetId())
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	in.Generation = nextGeneration(prev, in)
	conds := conditionsOf(prev.GetStatus().GetConditions())
	gen := int64(in.GetGeneration())
	accepted := conditions.Condition{Type: ConditionAccepted, Status: conditions.True, Reason: ReasonAccepted, Message: "the record names a known runtime and an absolute path", ObservedGeneration: gen}
	switch _, rerr := m.Runtimes.Get(in.GetRuntimeId()); {
	case rerr != nil:
		accepted.Status, accepted.Reason, accepted.Message = conditions.False, ReasonUnknownRuntime, rerr.Error()
	case !filepath.IsAbs(in.GetPath()):
		accepted.Status, accepted.Reason, accepted.Message = conditions.False, ReasonRelativePath, "path "+in.GetPath()+" is not absolute"
	}
	conditions.Set(&conds, accepted)
	ready := conditions.Condition{Type: ConditionReady, Status: conditions.True, Reason: ReasonBinaryPresent, Message: in.GetPath() + " is an executable file", ObservedGeneration: gen}
	switch info, serr := os.Stat(in.GetPath()); {
	case serr != nil:
		ready.Status, ready.Reason, ready.Message = conditions.False, ReasonBinaryMissing, serr.Error()
	case info.IsDir() || info.Mode()&0o111 == 0:
		ready.Status, ready.Reason, ready.Message = conditions.False, ReasonNotExecutable, in.GetPath()+" is not an executable file"
	}
	conditions.Set(&conds, ready)
	in.Status = &v1.InstallStatus{Conditions: protoConditions(conds), ObservedGeneration: in.GetGeneration()}
	return nil
}

// The generation the record takes, one more than stored when the path, directory, version, origin, kind or build changed
func nextGeneration(prev, in *v1.Install) uint64 {
	if prev == nil {
		return 1
	}
	gen := max(prev.GetGeneration(), 1)
	if prev.GetPath() != in.GetPath() || prev.GetDir() != in.GetDir() || prev.GetVersion() != in.GetVersion() || prev.GetOrigin() != in.GetOrigin() || prev.GetKind() != in.GetKind() || prev.GetBuildId() != in.GetBuildId() {
		gen++
	}
	return gen
}

func protoConditions(in []conditions.Condition) []*v1.Condition {
	out := make([]*v1.Condition, len(in))
	for i, c := range in {
		status := v1.ConditionStatus_CONDITION_STATUS_UNKNOWN
		switch c.Status {
		case conditions.True:
			status = v1.ConditionStatus_CONDITION_STATUS_TRUE
		case conditions.False:
			status = v1.ConditionStatus_CONDITION_STATUS_FALSE
		}
		out[i] = &v1.Condition{Type: c.Type, Status: status, Reason: c.Reason, Message: c.Message, LastTransitionTime: timestamppb.New(c.LastTransitionTime), ObservedGeneration: uint64(c.ObservedGeneration)}
	}
	return out
}

// Conditions read back from a stored status, keeping their transition times
func conditionsOf(in []*v1.Condition) []conditions.Condition {
	out := make([]conditions.Condition, 0, len(in))
	for _, c := range in {
		status := conditions.Unknown
		switch c.GetStatus() {
		case v1.ConditionStatus_CONDITION_STATUS_TRUE:
			status = conditions.True
		case v1.ConditionStatus_CONDITION_STATUS_FALSE:
			status = conditions.False
		}
		out = append(out, conditions.Condition{Type: c.GetType(), Status: status, Reason: c.GetReason(), Message: c.GetMessage(), LastTransitionTime: c.GetLastTransitionTime().AsTime(), ObservedGeneration: int64(c.GetObservedGeneration())})
	}
	return out
}
