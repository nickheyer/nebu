package conditions

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
	t2 = t0.Add(2 * time.Minute)
)

func TestSetTransitionTime(t *testing.T) {
	tests := []struct {
		name        string
		start       []Condition
		set         Condition
		wantChanged bool
		want        Condition
	}{
		{
			name:        "new condition takes its own time",
			set:         Condition{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t0, ObservedGeneration: 1},
			wantChanged: true,
			want:        Condition{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t0, ObservedGeneration: 1},
		},
		{
			name:        "same status keeps the old time and updates the rest",
			start:       []Condition{{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t0, ObservedGeneration: 1}},
			set:         Condition{Type: "Ready", Status: True, Reason: "NoReadyEndpoints", Message: "m", LastTransitionTime: t1, ObservedGeneration: 2},
			wantChanged: true,
			want:        Condition{Type: "Ready", Status: True, Reason: "NoReadyEndpoints", Message: "m", LastTransitionTime: t0, ObservedGeneration: 2},
		},
		{
			name:        "identical condition reports no change",
			start:       []Condition{{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t0, ObservedGeneration: 1}},
			set:         Condition{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t2, ObservedGeneration: 1},
			wantChanged: false,
			want:        Condition{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t0, ObservedGeneration: 1},
		},
		{
			name:        "status change moves the time",
			start:       []Condition{{Type: "Ready", Status: True, Reason: "Ready", LastTransitionTime: t0}},
			set:         Condition{Type: "Ready", Status: False, Reason: "NoReadyEndpoints", LastTransitionTime: t2},
			wantChanged: true,
			want:        Condition{Type: "Ready", Status: False, Reason: "NoReadyEndpoints", LastTransitionTime: t2},
		},
		{
			name:        "unknown to true moves the time",
			start:       []Condition{{Type: "Accepted", Status: Unknown, LastTransitionTime: t0}},
			set:         Condition{Type: "Accepted", Status: True, Reason: "Accepted", LastTransitionTime: t1},
			wantChanged: true,
			want:        Condition{Type: "Accepted", Status: True, Reason: "Accepted", LastTransitionTime: t1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conds := append([]Condition(nil), tc.start...)
			if got := Set(&conds, tc.set); got != tc.wantChanged {
				t.Fatalf("changed = %v, want %v", got, tc.wantChanged)
			}
			if len(conds) != 1 {
				t.Fatalf("len = %d, want 1", len(conds))
			}
			if !reflect.DeepEqual(conds[0], tc.want) {
				t.Fatalf("got %+v\nwant %+v", conds[0], tc.want)
			}
		})
	}
}

func TestSetStampsZeroTime(t *testing.T) {
	var conds []Condition
	before := time.Now()
	Set(&conds, Condition{Type: "Ready", Status: True, Reason: "Ready"})
	if conds[0].LastTransitionTime.Before(before) {
		t.Fatalf("transition time %v not stamped", conds[0].LastTransitionTime)
	}
	stamped := conds[0].LastTransitionTime
	Set(&conds, Condition{Type: "Ready", Status: True, Reason: "NoReadyEndpoints"})
	if !conds[0].LastTransitionTime.Equal(stamped) {
		t.Fatalf("time moved without a status change")
	}
}

func TestGetRemoveIsTrue(t *testing.T) {
	conds := []Condition{
		{Type: "Accepted", Status: True, Reason: "Accepted"},
		{Type: "Ready", Status: False, Reason: "NoReadyEndpoints"},
	}
	if !IsTrue(conds, "Accepted") || IsTrue(conds, "Ready") || IsTrue(conds, "Missing") {
		t.Fatal("IsTrue answers wrong")
	}
	if c, ok := Get(conds, "Ready"); !ok || c.Status != False {
		t.Fatalf("Get Ready = %+v %v", c, ok)
	}
	if _, ok := Get(conds, "Missing"); ok {
		t.Fatal("Get found a missing condition")
	}
	if !Remove(&conds, "Accepted") || Remove(&conds, "Accepted") {
		t.Fatal("Remove answers wrong")
	}
	if len(conds) != 1 || conds[0].Type != "Ready" {
		t.Fatalf("after remove %+v", conds)
	}
}

func TestConditionJSON(t *testing.T) {
	b, err := json.Marshal(Condition{Type: "Ready", Status: True, Reason: "Ready", Message: "M", LastTransitionTime: t0, ObservedGeneration: 3})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"type", "status", "reason", "message", "lastTransitionTime", "observedGeneration"}
	if len(m) != len(want) {
		t.Fatalf("keys %v", m)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
	b, err = json.Marshal(ParentStatus{ParentRef: ParentRef{Group: "g", Kind: "k", Name: "n", Namespace: "ns"}, ControllerName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	const wantParent = `{"parentRef":{"group":"g","kind":"k","name":"n","namespace":"ns"},"controllerName":"c","conditions":null}`
	if string(b) != wantParent {
		t.Fatalf("got %s\nwant %s", b, wantParent)
	}
}

func TestSetParentOwnershipIsolation(t *testing.T) {
	gw := ParentRef{Group: "nebu.dev", Kind: "Gateway", Name: "gw", Namespace: "default"}
	other := ParentRef{Group: "nebu.dev", Kind: "Gateway", Name: "gw2", Namespace: "default"}
	var list []ParentStatus

	mustSet := func(controller string, p ParentRef, c Condition) {
		t.Helper()
		if _, err := SetParent(&list, controller, p, c); err != nil {
			t.Fatal(err)
		}
	}
	mustSet("node-a", gw, Condition{Type: "Accepted", Status: True, Reason: "Accepted", LastTransitionTime: t0})
	mustSet("node-b", gw, Condition{Type: "Accepted", Status: False, Reason: "StoreFailed", LastTransitionTime: t0})
	mustSet("node-a", other, Condition{Type: "Accepted", Status: True, Reason: "Accepted", LastTransitionTime: t0})
	if len(list) != 3 {
		t.Fatalf("entries = %d, want 3", len(list))
	}

	mustSet("node-a", gw, Condition{Type: "Accepted", Status: False, Reason: "StoreFailed", LastTransitionTime: t1})

	a, _ := GetParent(list, "node-a", gw)
	b, _ := GetParent(list, "node-b", gw)
	o, _ := GetParent(list, "node-a", other)
	if c, _ := Get(a.Conditions, "Accepted"); c.Status != False || !c.LastTransitionTime.Equal(t1) {
		t.Fatalf("node-a gw = %+v", c)
	}
	if c, _ := Get(b.Conditions, "Accepted"); c.Status != False || c.Reason != "StoreFailed" || !c.LastTransitionTime.Equal(t0) {
		t.Fatalf("node-b gw touched: %+v", c)
	}
	if c, _ := Get(o.Conditions, "Accepted"); c.Status != True || !c.LastTransitionTime.Equal(t0) {
		t.Fatalf("node-a other touched: %+v", c)
	}

	if !RemoveParent(&list, "node-b", gw) || RemoveParent(&list, "node-b", gw) {
		t.Fatal("RemoveParent answers wrong")
	}
	if _, ok := GetParent(list, "node-a", gw); !ok {
		t.Fatal("RemoveParent removed another controller's entry")
	}
	if n := RemoveController(&list, "node-a"); n != 2 || len(list) != 0 {
		t.Fatalf("RemoveController removed %d, left %d", n, len(list))
	}
}

func TestSetParentCap(t *testing.T) {
	var list []ParentStatus
	c := Condition{Type: "Accepted", Status: True, Reason: "Accepted", LastTransitionTime: t0}
	for i := range MaxParents {
		if _, err := SetParent(&list, "ctl", ParentRef{Kind: "Gateway", Name: fmt.Sprint(i)}, c); err != nil {
			t.Fatalf("parent %d: %v", i, err)
		}
	}
	if _, err := SetParent(&list, "ctl", ParentRef{Kind: "Gateway", Name: "overflow"}, c); !errors.Is(err, ErrTooManyParents) {
		t.Fatalf("err = %v, want ErrTooManyParents", err)
	}
	if len(list) != MaxParents {
		t.Fatalf("len = %d after refused add", len(list))
	}
	changed, err := SetParent(&list, "ctl", ParentRef{Kind: "Gateway", Name: "0"}, Condition{Type: "Accepted", Status: False, Reason: "StoreFailed", LastTransitionTime: t1})
	if err != nil || !changed {
		t.Fatalf("updating an existing entry at the cap: changed=%v err=%v", changed, err)
	}
}

// Every resource declares its types, each type lists its reasons, and Set refuses a reason or type outside them
func TestDeclaredSetsEnforced(t *testing.T) {
	for _, resource := range []string{ResourcePool, ResourceRoute, ResourceSchedulerConfig, ResourceInstance, ResourceSlot, ResourceFormation, ResourceImport} {
		types := Types(resource)
		if len(types) == 0 {
			t.Fatalf("%s declares no condition type", resource)
		}
		for _, typ := range types {
			reasons := Reasons(resource, typ)
			if len(reasons) == 0 {
				t.Fatalf("%s %s allows no reason", resource, typ)
			}
			var conds []Condition
			var protos []*v1.Condition
			for _, reason := range reasons {
				Set(&conds, Condition{Type: typ, Status: True, Reason: reason})
				SetProto(&protos, Condition{Type: typ, Status: True, Reason: reason})
			}
			if len(conds) != 1 || conds[0].Reason != reasons[len(reasons)-1] || len(protos) != 1 || protos[0].GetReason() != reasons[len(reasons)-1] {
				t.Fatalf("%s %s set %v %v", resource, typ, conds, protos)
			}
		}
	}
	refused := func(name string, c Condition) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s accepted", name)
			}
		}()
		var conds []Condition
		Set(&conds, c)
	}
	refused("unlisted reason", Condition{Type: Ready, Status: True, Reason: "Whatever"})
	refused("undeclared type", Condition{Type: "Shiny", Status: True, Reason: "Ready"})
	refused("empty reason", Condition{Type: Accepted, Status: True})
	defer func() {
		if recover() == nil {
			t.Fatal("proto set accepted an unlisted reason")
		}
	}()
	var protos []*v1.Condition
	SetProto(&protos, Condition{Type: Exported, Status: True, Reason: "Ready"})
}

// Proto conditions round trip through the struct form and flip their transition time only on a status change
func TestProtoRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var protos []*v1.Condition
	if !SetProto(&protos, Condition{Type: Ready, Status: True, Reason: "Ready", Message: "m", LastTransitionTime: t0, ObservedGeneration: 2}) {
		t.Fatal("first set changed nothing")
	}
	got := FromProto(protos[0])
	if got != (Condition{Type: Ready, Status: True, Reason: "Ready", Message: "m", LastTransitionTime: t0, ObservedGeneration: 2}) {
		t.Fatalf("round trip %+v", got)
	}
	if SetProto(&protos, Condition{Type: Ready, Status: True, Reason: "Ready", Message: "m", LastTransitionTime: t0.Add(time.Hour), ObservedGeneration: 2}) {
		t.Fatal("an unchanged set reported a change")
	}
	if !SetProto(&protos, Condition{Type: Ready, Status: False, Reason: "NoReadyEndpoints", LastTransitionTime: t0.Add(time.Hour)}) || !protos[0].GetLastTransitionTime().AsTime().Equal(t0.Add(time.Hour)) {
		t.Fatalf("status flip kept the old transition time %v", protos[0])
	}
	if StatusOf(ToProto(Condition{Status: Unknown, Type: Ready, Reason: "Ready"}).GetStatus()) != Unknown {
		t.Fatal("unknown status lost")
	}
}
