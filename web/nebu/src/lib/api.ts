import { createClient, ConnectError, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { BuildService } from '$proto/recipe_pb';
import { EstimateService } from '$proto/estimate_pb';
import { EventService } from '$proto/event_pb';
import { GatewayService } from '$proto/gateway_pb';
import { HostService } from '$proto/host_pb';
import { InstanceService } from '$proto/instance_pb';
import { MonitorService } from '$proto/monitor_pb';
import { RuntimeService } from '$proto/runtime_pb';
import { SettingsService } from '$proto/settings_pb';
import { SlotService } from '$proto/slot_pb';
import { SourceService } from '$proto/source_pb';
import { StoreService } from '$proto/store_pb';
import { TaskService } from '$proto/task_pb';

const tokenKey = 'nebu.token';
const gatewayKeyKey = 'nebu.gateway_key';

// Returns the gateway key the user saved in this browser, for the chat page
export function gatewayKey(): string {
  try {
    return localStorage.getItem(gatewayKeyKey) ?? '';
  } catch {
    return '';
  }
}

export function setGatewayKey(value: string) {
  try {
    if (value) localStorage.setItem(gatewayKeyKey, value);
    else localStorage.removeItem(gatewayKeyKey);
  } catch {
    // storage may be unavailable in private windows
  }
}

// Returns the API token the user saved in this browser
export function token(): string {
  try {
    return localStorage.getItem(tokenKey) ?? '';
  } catch {
    return '';
  }
}

// Saves the API token for later requests
export function setToken(value: string) {
  try {
    if (value) localStorage.setItem(tokenKey, value);
    else localStorage.removeItem(tokenKey);
  } catch {
    // storage may be unavailable in private windows
  }
}

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
  monitor: createClient(MonitorService, transport),
  events: createClient(EventService, transport)
};

// Formats a Connect error for people
export function message(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message.replace(/^\[\w+\]\s*/, '');
  return String(err);
}

// Reports whether an error is the daemon refusing the token
export function unauthenticated(err: unknown): boolean {
  return err instanceof ConnectError && err.code === 16;
}
