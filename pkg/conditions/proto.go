package conditions

import (
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Upserts a proto condition by type under the same reason table as Set, true when anything changed
func SetProto(conds *[]*v1.Condition, c Condition) bool {
	check(c)
	status := ProtoStatus(c.Status)
	if c.LastTransitionTime.IsZero() {
		c.LastTransitionTime = time.Now().UTC()
	}
	for _, cur := range *conds {
		if cur.GetType() != c.Type {
			continue
		}
		changed := cur.GetStatus() != status || cur.GetReason() != c.Reason || cur.GetMessage() != c.Message || cur.GetObservedGeneration() != uint64(c.ObservedGeneration)
		if cur.GetStatus() != status {
			cur.LastTransitionTime = timestamppb.New(c.LastTransitionTime)
		}
		cur.Status, cur.Reason, cur.Message, cur.ObservedGeneration = status, c.Reason, c.Message, uint64(c.ObservedGeneration)
		return changed
	}
	*conds = append(*conds, ToProto(c))
	return true
}

// The proto spelling of a status
func ProtoStatus(s Status) v1.ConditionStatus {
	switch s {
	case True:
		return v1.ConditionStatus_CONDITION_STATUS_TRUE
	case False:
		return v1.ConditionStatus_CONDITION_STATUS_FALSE
	case Unknown:
		return v1.ConditionStatus_CONDITION_STATUS_UNKNOWN
	}
	return v1.ConditionStatus_CONDITION_STATUS_UNSPECIFIED
}

// The status a proto value spells, Unknown for one not True or False
func StatusOf(s v1.ConditionStatus) Status {
	switch s {
	case v1.ConditionStatus_CONDITION_STATUS_TRUE:
		return True
	case v1.ConditionStatus_CONDITION_STATUS_FALSE:
		return False
	}
	return Unknown
}

// A condition as the API carries it
func ToProto(c Condition) *v1.Condition {
	return &v1.Condition{
		Type:               c.Type,
		Status:             ProtoStatus(c.Status),
		Reason:             c.Reason,
		Message:            c.Message,
		LastTransitionTime: timestamppb.New(c.LastTransitionTime),
		ObservedGeneration: uint64(c.ObservedGeneration),
	}
}

// A condition read back from the API, keeping its transition time
func FromProto(c *v1.Condition) Condition {
	return Condition{
		Type:               c.GetType(),
		Status:             StatusOf(c.GetStatus()),
		Reason:             c.GetReason(),
		Message:            c.GetMessage(),
		LastTransitionTime: c.GetLastTransitionTime().AsTime(),
		ObservedGeneration: int64(c.GetObservedGeneration()),
	}
}
