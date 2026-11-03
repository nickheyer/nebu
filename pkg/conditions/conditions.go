// Package conditions keeps typed status conditions and per parent, per controller status entries
package conditions

import (
	"errors"
	"slices"
	"time"
)

// Whether a condition holds
type Status string

const (
	True    Status = "True"
	False   Status = "False"
	Unknown Status = "Unknown"
)

// One observed aspect of a resource and when it last changed
type Condition struct {
	Type               string    `json:"type"`
	Status             Status    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
	ObservedGeneration int64     `json:"observedGeneration"`
}

// Upserts by type under the declared reason table, moving the transition time only on a status change, true when anything changed
func Set(conds *[]Condition, c Condition) bool {
	check(c)
	if c.LastTransitionTime.IsZero() {
		c.LastTransitionTime = time.Now().UTC()
	}
	for i := range *conds {
		cur := &(*conds)[i]
		if cur.Type != c.Type {
			continue
		}
		if cur.Status != c.Status {
			*cur = c
			return true
		}
		changed := cur.Reason != c.Reason || cur.Message != c.Message || cur.ObservedGeneration != c.ObservedGeneration
		cur.Reason, cur.Message, cur.ObservedGeneration = c.Reason, c.Message, c.ObservedGeneration
		return changed
	}
	*conds = append(*conds, c)
	return true
}

// The condition of a type and whether it is present
func Get(conds []Condition, typ string) (Condition, bool) {
	for _, c := range conds {
		if c.Type == typ {
			return c, true
		}
	}
	return Condition{}, false
}

// Deletes the condition of a type, true when one was present
func Remove(conds *[]Condition, typ string) bool {
	n := len(*conds)
	*conds = slices.DeleteFunc(*conds, func(c Condition) bool { return c.Type == typ })
	return len(*conds) != n
}

// Whether the condition of a type is present with status True
func IsTrue(conds []Condition, typ string) bool {
	c, ok := Get(conds, typ)
	return ok && c.Status == True
}

// Most parent entries a resource status holds
const MaxParents = 32

// Returned when adding a parent entry would exceed the cap
var ErrTooManyParents = errors.New("status already holds the maximum number of parent entries")

// The resource a status entry reports against
type ParentRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// The conditions one controller observed for one parent
type ParentStatus struct {
	ParentRef      ParentRef   `json:"parentRef"`
	ControllerName string      `json:"controllerName"`
	Conditions     []Condition `json:"conditions"`
}

// Index of the entry owned by controller for parent, minus one when absent
func indexParent(list []ParentStatus, controller string, parent ParentRef) int {
	return slices.IndexFunc(list, func(p ParentStatus) bool {
		return p.ControllerName == controller && p.ParentRef == parent
	})
}

// The entry owned by controller for parent and whether it is present
func GetParent(list []ParentStatus, controller string, parent ParentRef) (ParentStatus, bool) {
	i := indexParent(list, controller, parent)
	if i < 0 {
		return ParentStatus{}, false
	}
	return list[i], true
}

// Sets a condition on the entry owned by controller for parent, creating it within the cap
func SetParent(list *[]ParentStatus, controller string, parent ParentRef, c Condition) (bool, error) {
	i := indexParent(*list, controller, parent)
	if i < 0 {
		if len(*list) >= MaxParents {
			return false, ErrTooManyParents
		}
		*list = append(*list, ParentStatus{ParentRef: parent, ControllerName: controller})
		i = len(*list) - 1
	}
	return Set(&(*list)[i].Conditions, c), nil
}

// Deletes the entry owned by controller for parent, true when one was present
func RemoveParent(list *[]ParentStatus, controller string, parent ParentRef) bool {
	n := len(*list)
	*list = slices.DeleteFunc(*list, func(p ParentStatus) bool {
		return p.ControllerName == controller && p.ParentRef == parent
	})
	return len(*list) != n
}

// Deletes every entry a controller owns once it stops managing the resource, the count removed
func RemoveController(list *[]ParentStatus, controller string) int {
	n := len(*list)
	*list = slices.DeleteFunc(*list, func(p ParentStatus) bool { return p.ControllerName == controller })
	return n - len(*list)
}
