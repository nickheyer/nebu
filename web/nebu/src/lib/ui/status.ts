import { BotState } from '#lib/proto/nebu/v1/bot_pb.js';
import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
import { FormationState, NodeState, AdmissionState } from '#lib/proto/nebu/v1/mesh_pb.js';
import { BuildState } from '#lib/proto/nebu/v1/recipe_pb.js';
import { SlotState } from '#lib/proto/nebu/v1/slot_pb.js';
import { TaskState } from '#lib/proto/nebu/v1/task_pb.js';
import { ToolServerState } from '#lib/proto/nebu/v1/tools_pb.js';

// A condition reason as lowercase words
export function reasonWords(reason: string): string {
	return reason
		.replace(/([a-z0-9])([A-Z])/g, '$1 $2')
		.replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2')
		.replaceAll('_', ' ')
		.toLowerCase();
}

// The lamp a state lights
export type Lamp = 'success' | 'warning' | 'error' | 'primary' | 'muted';

// Filled for live states, half for starting ones, hollow for idle ones
export type DotShape = 'filled' | 'half' | 'hollow';

export function instanceLamp(s: InstanceState): Lamp {
	switch (s) {
		case InstanceState.READY:
			return 'success';
		case InstanceState.STARTING:
			return 'warning';
		case InstanceState.STOPPING:
		case InstanceState.DRAINING:
			return 'primary';
		case InstanceState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function instanceShape(s: InstanceState): DotShape {
	if (s === InstanceState.STARTING) return 'half';
	if (s === InstanceState.IDLE || s === InstanceState.STOPPED || s === InstanceState.UNSPECIFIED) return 'hollow';
	return 'filled';
}

export function slotLamp(s: SlotState): Lamp {
	switch (s) {
		case SlotState.READY:
			return 'success';
		case SlotState.STARTING:
		case SlotState.SWAPPING:
			return 'warning';
		case SlotState.DRAINING:
			return 'primary';
		case SlotState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function slotShape(s: SlotState): DotShape {
	if (s === SlotState.STARTING || s === SlotState.SWAPPING) return 'half';
	if (s === SlotState.EMPTY || s === SlotState.IDLE || s === SlotState.UNSPECIFIED) return 'hollow';
	return 'filled';
}

export function routeLamp(s: RouteState): Lamp {
	switch (s) {
		case RouteState.READY:
			return 'success';
		case RouteState.PENDING:
			return 'warning';
		case RouteState.DRAINING:
			return 'primary';
		default:
			return 'muted';
	}
}

export function routeShape(s: RouteState): DotShape {
	return s === RouteState.PENDING ? 'half' : s === RouteState.READY ? 'filled' : 'hollow';
}

export function taskLamp(s: TaskState): Lamp {
	switch (s) {
		case TaskState.SUCCEEDED:
			return 'success';
		case TaskState.PENDING:
		case TaskState.RUNNING:
			return 'warning';
		case TaskState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function buildLamp(s: BuildState): Lamp {
	switch (s) {
		case BuildState.SUCCEEDED:
			return 'success';
		case BuildState.RUNNING:
			return 'warning';
		case BuildState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function nodeLamp(s: NodeState): Lamp {
	switch (s) {
		case NodeState.READY:
			return 'success';
		case NodeState.UNREACHABLE:
			return 'error';
		default:
			return 'muted';
	}
}

export function formationLamp(s: FormationState): Lamp {
	switch (s) {
		case FormationState.READY:
			return 'success';
		case FormationState.STARTING:
		case FormationState.DEGRADED:
			return 'warning';
		case FormationState.STOPPING:
			return 'primary';
		case FormationState.FAILED:
		case FormationState.UNREACHABLE:
			return 'error';
		default:
			return 'muted';
	}
}

export function botLamp(s: BotState): Lamp {
	switch (s) {
		case BotState.READY:
			return 'success';
		case BotState.CONNECTING:
		case BotState.DEGRADED:
			return 'warning';
		case BotState.STOPPING:
			return 'primary';
		case BotState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function toolLamp(s: ToolServerState): Lamp {
	switch (s) {
		case ToolServerState.READY:
			return 'success';
		case ToolServerState.CONNECTING:
			return 'warning';
		case ToolServerState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function admissionLamp(s: AdmissionState): Lamp {
	switch (s) {
		case AdmissionState.JOINED:
		case AdmissionState.ADMITTED:
			return 'success';
		case AdmissionState.PENDING:
			return 'warning';
		case AdmissionState.DENIED:
		case AdmissionState.DECLINED:
		case AdmissionState.FAILED:
			return 'error';
		default:
			return 'muted';
	}
}

export function statusClassLamp(status: number): Lamp {
	if (status >= 500) return 'error';
	if (status >= 400) return 'warning';
	if (status >= 200) return 'success';
	return 'muted';
}

export function levelLamp(level: string): Lamp {
	if (level === 'error') return 'error';
	if (level === 'warn') return 'warning';
	return 'muted';
}
