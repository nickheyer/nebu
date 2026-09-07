import { createClient, ConnectError, Code, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { BuildService } from '$proto/recipe_pb';
import { EstimateService } from '$proto/estimate_pb';
import { EventService } from '$proto/event_pb';
import { GatewayService } from '$proto/gateway_pb';
import { HostService } from '$proto/host_pb';
import { InstanceService } from '$proto/instance_pb';
import { RuntimeService } from '$proto/runtime_pb';
import { SettingsService } from '$proto/settings_pb';
import { SlotService } from '$proto/slot_pb';
import { SourceService } from '$proto/source_pb';
import { StoreService } from '$proto/store_pb';
import { TaskService } from '$proto/task_pb';
import { readLocal, writeLocal } from './persist';

const tokenKey = 'nebu.token';
const gatewayKeyKey = 'nebu.gateway_key';

// The gateway key saved in this browser, for the chat page
export const gatewayKey = () => readLocal(gatewayKeyKey);
export const setGatewayKey = (value: string) => writeLocal(gatewayKeyKey, value);

// The API token saved in this browser
export const token = () => readLocal(tokenKey);
export const setToken = (value: string) => writeLocal(tokenKey, value);

const auth: Interceptor = (next) => async (req) => {
  const t = token();
  if (t) req.header.set('Authorization', 'Bearer ' + t);
  return next(req);
};

export const baseUrl = typeof window === 'undefined' ? 'http://127.0.0.1:8484' : window.location.origin;

const transport = createConnectTransport({ baseUrl, interceptors: [auth] });

// One client per service, sharing the transport
export const api = {
  host: createClient(HostService, transport),
  settings: createClient(SettingsService, transport),
  sources: createClient(SourceService, transport),
  runtimes: createClient(RuntimeService, transport),
  estimate: createClient(EstimateService, transport),
  store: createClient(StoreService, transport),
  tasks: createClient(TaskService, transport),
  instances: createClient(InstanceService, transport),
  builds: createClient(BuildService, transport),
  slots: createClient(SlotService, transport),
  gateway: createClient(GatewayService, transport),
  events: createClient(EventService, transport)
};

// Formats an error for people
export function message(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message.replace(/^\[\w+\]\s*/, '');
  return String(err);
}

// The Connect code of an error, unknown for anything else
export function code(err: unknown): Code | undefined {
  return err instanceof ConnectError ? err.code : undefined;
}

// Whether the daemon refused the token
export function unauthenticated(err: unknown): boolean {
  return code(err) === Code.Unauthenticated;
}
