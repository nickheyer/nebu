import { createClient, ConnectError, Code, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { AuthService } from '$proto/auth_pb';
import { BuildService } from '$proto/recipe_pb';
import { BotService } from '$proto/bot_pb';
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

export const gatewayKey = () => readLocal(gatewayKeyKey);
export const setGatewayKey = (value: string) => writeLocal(gatewayKeyKey, value);

export const token = () => readLocal(tokenKey);
export const setToken = (value: string) => writeLocal(tokenKey, value);

const auth: Interceptor = (next) => async (req) => {
  const t = token();
  if (t) req.header.set('Authorization', 'Bearer ' + t);
  return next(req);
};

export const baseUrl = typeof window === 'undefined' ? 'http://127.0.0.1:8484' : window.location.origin;

const transport = createConnectTransport({ baseUrl, interceptors: [auth] });

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
  events: createClient(EventService, transport),
  bots: createClient(BotService, transport),
  auth: createClient(AuthService, transport)
};

// Include the API token in file URLs when authentication is required.
export function fileUrl(sourceId: string, repo: string, revision: string, path: string): string {
  const p = new URLSearchParams({ source: sourceId, repo, path });
  if (revision) p.set('revision', revision);
  const t = token();
  if (t) p.set('token', t);
  return `${baseUrl}/files?${p.toString()}`;
}

export function message(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message.replace(/^\[\w+\]\s*/, '');
  return String(err);
}

export function code(err: unknown): Code | undefined {
  return err instanceof ConnectError ? err.code : undefined;
}

export function unauthenticated(err: unknown): boolean {
  return code(err) === Code.Unauthenticated;
}
