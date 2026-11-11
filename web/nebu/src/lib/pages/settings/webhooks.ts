import { NotifyEvent, WebhookFormat } from '#lib/proto/nebu/v1/settings_pb.js';

export const webhookEvents = [
	{ value: NotifyEvent.MODEL_FAILED, label: 'Model failed' },
	{ value: NotifyEvent.TASK_FAILED, label: 'Task failed' },
	{ value: NotifyEvent.TASK_SUCCEEDED, label: 'Task finished' }
];

export const webhookFormats = [
	{ value: WebhookFormat.EVENT, label: 'Event JSON', description: 'The event document' },
	{ value: WebhookFormat.DISCORD, label: 'Discord', description: 'A line of text' },
	{ value: WebhookFormat.SLACK, label: 'Slack', description: 'A line of text, Mattermost and Rocket.Chat too' }
];
