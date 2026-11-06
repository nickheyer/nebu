import type { NeoNotification, NeoNotificationQueued, NeoNotificationStackService } from '#lib/neo/floating/notification/index.js';

let service: NeoNotificationStackService | undefined;

export function bindNotifications(s: NeoNotificationStackService): void {
	service = s;
}

function add(type: 'info' | 'success' | 'warning' | 'error', title: string, content?: string): NeoNotificationQueued {
	if (!service) throw new Error('notifications are not mounted');
	return service.add({ type, title, content, duration: type === 'error' ? 10_000 : 5_000, close: true });
}

export function notify(item: NeoNotification): NeoNotificationQueued {
	if (!service) throw new Error('notifications are not mounted');
	return service.add(item);
}

export const toast = {
	info: (title: string, content?: string) => add('info', title, content),
	success: (title: string, content?: string) => add('success', title, content),
	warning: (title: string, content?: string) => add('warning', title, content),
	error: (title: string, content?: string) => add('error', title, content)
};
