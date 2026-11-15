import { NotifyEvent, ToolServerKind, WebhookFormat } from '#lib/proto/nebu/v1/settings_pb.js';

export const webhookEvents = [
	{ value: NotifyEvent.MODEL_FAILED, label: 'Model failed' },
	{ value: NotifyEvent.TASK_FAILED, label: 'Task failed' },
	{ value: NotifyEvent.TASK_SUCCEEDED, label: 'Task succeeded' }
];

export const webhookFormats = [
	{ value: WebhookFormat.EVENT, label: 'Event JSON', description: 'The event document' },
	{ value: WebhookFormat.DISCORD, label: 'Discord', description: 'A line of text' },
	{ value: WebhookFormat.SLACK, label: 'Slack', description: 'A line of text, Mattermost and Rocket.Chat too' }
];

// The kinds of tool server by the names the list and the editor share
export const toolKinds = [
	{ value: ToolServerKind.SEARCH, label: 'Web search', description: 'SearXNG or Brave' },
	{ value: ToolServerKind.FETCH, label: 'Page fetch', description: 'Pages as text' },
	{ value: ToolServerKind.STDIO, label: 'MCP over stdio', description: 'A command nebu runs' },
	{ value: ToolServerKind.HTTP, label: 'MCP over HTTP', description: 'A server at a URL' }
];
