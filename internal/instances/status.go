package instances

import (
	"github.com/nickheyer/nebu/pkg/conditions"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Condition types an instance reports
const (
	CondScheduled = "Scheduled"
	CondLaunched  = "Launched"
	CondReady     = "Ready"
	CondHealthy   = "Healthy"
	CondDraining  = "Draining"
	CondSnapshot  = "Snapshotted"
)

// Reasons the instance conditions carry
const (
	ReasonPlanned        = "Planned"
	ReasonPlanFailed     = "PlanFailed"
	ReasonLaunching      = "Launching"
	ReasonRunning        = "ProcessRunning"
	ReasonLaunchFailed   = "LaunchFailed"
	ReasonCrashed        = "Crashed"
	ReasonDrainCrashed   = "CrashedWhileDraining"
	ReasonStopped        = "Stopped"
	ReasonIdle           = "IdleUnloaded"
	ReasonAsleep         = "Asleep"
	ReasonPublished      = "Published"
	ReasonStarting       = "Starting"
	ReasonProbeOK        = "ProbeSucceeded"
	ReasonProbeFailed    = "ProbeFailed"
	ReasonStopRequested  = "StopRequested"
	ReasonRequestChanged = "RequestChanged"
	ReasonShutdown       = "DaemonClosing"
	ReasonServing        = "Serving"
	ReasonFailedLaunches = "TooManyFailedLaunches"
	ReasonSnapshotted    = "Snapshotted"
	ReasonSnapshotFailed = "SnapshotFailed"
	ReasonRestored       = "RestoredFromSnapshot"
	ReasonRestoreFailed  = "RestoreFailed"
)

// Upserts a condition by type under the declared reason table, moving its transition time only when the status flips
func SetCondition(conds []*v1.Condition, typ string, ok bool, reason, message string, generation uint64) []*v1.Condition {
	status := conditions.False
	if ok {
		status = conditions.True
	}
	conditions.SetProto(&conds, conditions.Condition{Type: typ, Status: status, Reason: reason, Message: message, ObservedGeneration: int64(generation)})
	return conds
}

// Whether the condition of a type is present and true
func ConditionTrue(conds []*v1.Condition, typ string) bool {
	for _, c := range conds {
		if c.GetType() == typ {
			return c.GetStatus() == v1.ConditionStatus_CONDITION_STATUS_TRUE
		}
	}
	return false
}

// The condition of a type, nil when absent
func ConditionOf(conds []*v1.Condition, typ string) *v1.Condition {
	for _, c := range conds {
		if c.GetType() == typ {
			return c
		}
	}
	return nil
}
