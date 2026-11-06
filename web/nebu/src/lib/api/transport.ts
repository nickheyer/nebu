import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { AuthService } from '#lib/proto/nebu/v1/auth_pb.js';
import { BotService } from '#lib/proto/nebu/v1/bot_pb.js';
import { ChatService } from '#lib/proto/nebu/v1/chat_pb.js';
import { EstimateService } from '#lib/proto/nebu/v1/estimate_pb.js';
import { EventService } from '#lib/proto/nebu/v1/event_pb.js';
import { GatewayService } from '#lib/proto/nebu/v1/gateway_pb.js';
import { HostService } from '#lib/proto/nebu/v1/host_pb.js';
import { InstanceService } from '#lib/proto/nebu/v1/instance_pb.js';
import { MeshService } from '#lib/proto/nebu/v1/mesh_pb.js';
import { PoolImportService } from '#lib/proto/nebu/v1/poolimport_pb.js';
import { BuildService } from '#lib/proto/nebu/v1/recipe_pb.js';
import { RuntimeService } from '#lib/proto/nebu/v1/runtime_pb.js';
import { SettingsService } from '#lib/proto/nebu/v1/settings_pb.js';
import { SlotService } from '#lib/proto/nebu/v1/slot_pb.js';
import { SourceService } from '#lib/proto/nebu/v1/source_pb.js';
import { StoreService } from '#lib/proto/nebu/v1/store_pb.js';
import { TaskService } from '#lib/proto/nebu/v1/task_pb.js';
import { ToolService } from '#lib/proto/nebu/v1/tools_pb.js';

const expired = new Set<() => void>();

// Runs the registered listeners whenever the daemon answers Unauthenticated
const sessionWatch: Interceptor = (next) => async (req) => {
	try {
		return await next(req);
	} catch (err) {
		if (err instanceof ConnectError && err.code === Code.Unauthenticated) expired.forEach((fn) => fn());
		throw err;
	}
};

export function onSessionExpired(fn: () => void): () => void {
	expired.add(fn);
	return () => expired.delete(fn);
}

const transport = createConnectTransport({ baseUrl: '/', useBinaryFormat: true, interceptors: [sessionWatch] });

export const rpc = {
	auth: createClient(AuthService, transport),
	bots: createClient(BotService, transport),
	build: createClient(BuildService, transport),
	chat: createClient(ChatService, transport),
	estimate: createClient(EstimateService, transport),
	events: createClient(EventService, transport),
	gateway: createClient(GatewayService, transport),
	host: createClient(HostService, transport),
	imports: createClient(PoolImportService, transport),
	instances: createClient(InstanceService, transport),
	mesh: createClient(MeshService, transport),
	runtimes: createClient(RuntimeService, transport),
	settings: createClient(SettingsService, transport),
	slots: createClient(SlotService, transport),
	sources: createClient(SourceService, transport),
	store: createClient(StoreService, transport),
	tasks: createClient(TaskService, transport),
	tools: createClient(ToolService, transport)
};

// The daemon's own message for a failed call, or the error's text
export function reason(err: unknown): string {
	if (err instanceof ConnectError) return err.rawMessage;
	if (err instanceof Error) return err.message;
	return String(err);
}

export function isCode(err: unknown, code: Code): boolean {
	return err instanceof ConnectError && err.code === code;
}

export function isCancel(err: unknown): boolean {
	return (err instanceof DOMException && err.name === 'AbortError') || isCode(err, Code.Canceled);
}
