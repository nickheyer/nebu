package conditions

import (
	"fmt"
	"slices"
)

// Resource kinds whose condition sets are declared here
const (
	ResourcePool            = "Pool"
	ResourceRoute           = "Route"
	ResourceSchedulerConfig = "SchedulerConfig"
	ResourceInstance        = "Instance"
	ResourceSlot            = "Slot"
	ResourceFormation       = "Formation"
	ResourceImport          = "Import"
	ResourceObjective       = "Objective"
	ResourceInstall         = "Install"
)

// Condition types resources report
const (
	Accepted     = "Accepted"
	ResolvedRefs = "ResolvedRefs"
	Ready        = "Ready"
	Exported     = "Exported"
	Scheduled    = "Scheduled"
	Launched     = "Launched"
	Healthy      = "Healthy"
	Draining     = "Draining"
	Occupied     = "Occupied"
	Swapping     = "Swapping"
	Canary       = "Canary"
	Planned      = "Planned"
	Degraded     = "Degraded"
	Snapshotted  = "Snapshotted"
	Scraped      = "Scraped"
)

// The condition types each resource reports and the reasons each type allows
var sets = map[string]map[string][]string{
	ResourcePool: {
		Accepted:     {"Accepted", "UnknownSchedulerConfig", "InvalidSchedulerConfig"},
		ResolvedRefs: {"ResolvedRefs", "UnknownSchedulerConfig", "InvalidSchedulerConfig"},
		Ready:        {"Ready", "NoReadyEndpoints"},
		Exported:     {"Exported", "NotExported"},
		Scraped:      {"Scraped", "NotScraped", "ScrapeFailing"},
	},
	ResourceRoute: {
		Accepted:     {"Accepted", "StoreFailed"},
		ResolvedRefs: {"ResolvedRefs", "InvalidPoolRef"},
		Ready:        {"Ready", "NoReadyEndpoints"},
	},
	ResourceSchedulerConfig: {
		Accepted: {"Accepted", "Invalid"},
	},
	ResourceInstance: {
		Scheduled:   {"Planned", "PlanFailed"},
		Launched:    {"Launching", "ProcessRunning", "LaunchFailed", "Crashed", "CrashedWhileDraining", "Stopped", "IdleUnloaded", "TooManyFailedLaunches", "StopRequested", "RequestChanged", "DaemonClosing"},
		Ready:       {"Published", "Starting", "LaunchFailed", "Crashed", "Stopped", "IdleUnloaded", "Asleep", "StopRequested", "RequestChanged", "DaemonClosing", "Serving", "ProbeFailed"},
		Healthy:     {"ProbeSucceeded", "ProbeFailed", "Crashed"},
		Draining:    {"Starting", "Serving", "StopRequested", "RequestChanged", "DaemonClosing", "IdleUnloaded", "Asleep", "Crashed", "Stopped", "LaunchFailed"},
		Snapshotted: {"Snapshotted", "SnapshotFailed", "RestoredFromSnapshot", "RestoreFailed"},
	},
	ResourceSlot: {
		Accepted: {"Valid"},
		Occupied: {"Instance", "Formation", "Empty"},
		Ready:    {"OccupantReady", "SwapDraining", "OldOccupantServing", "OccupantStarting", "OccupantIdle", "OccupantDraining", "OccupantFailed", "Empty"},
		Swapping: {"BlueGreen", "DrainFirst", "Canary", "Settled"},
		Canary:   {"Launching", "Shifting", "Promoting", "Promoted", "RollingBack", "RolledBack", "Abandoned"},
	},
	ResourceFormation: {
		Planned:  {"Planned", "Launching", "LaunchFailed"},
		Launched: {"Launching", "SeatsReady", "LaunchFailed", "Stopping", "Stopped", "FormationDown"},
		Ready:    {"Routed", "Launching", "LaunchFailed", "Stopping", "Stopped", "FormationDown", "SeatFailed"},
		Degraded: {"Launching", "ServingWhole", "SeatFailed", "FormationDown", "Stopped"},
	},
	ResourceImport: {
		Ready: {"Ready", "NoReadyEndpoints", "PoolNotPublished"},
	},
	ResourceObjective: {
		Accepted: {"Accepted"},
		Ready:    {"Ready"},
	},
	ResourceInstall: {
		Accepted: {"Accepted", "UnknownRuntime", "RelativePath"},
		Ready:    {"BinaryPresent", "BinaryMissing", "NotExecutable"},
	},
}

// The reasons a condition type allows across every resource declaring it, nil for an undeclared type
func allowedReasons(typ string) []string {
	var out []string
	for _, set := range sets {
		out = append(out, set[typ]...)
	}
	return out
}

// Panics when the condition's type is declared by no resource or its reason is not one the type allows
func check(c Condition) {
	allowed := allowedReasons(c.Type)
	switch {
	case allowed == nil:
		panic(fmt.Sprintf("conditions: type %q is declared by no resource", c.Type))
	case !slices.Contains(allowed, c.Reason):
		panic(fmt.Sprintf("conditions: reason %q is not allowed for condition %s, allowed reasons are %v", c.Reason, c.Type, allowed))
	}
}

// The condition types a resource declares in sorted order
func Types(resource string) []string {
	set := sets[resource]
	out := make([]string, 0, len(set))
	for typ := range set {
		out = append(out, typ)
	}
	slices.Sort(out)
	return out
}

// The reasons a resource allows for one condition type
func Reasons(resource, typ string) []string {
	return slices.Clone(sets[resource][typ])
}
