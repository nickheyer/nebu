import { reason, rpc } from '#lib/api/transport.js';
import type { Policy } from '#lib/proto/nebu/v1/gateway_pb.js';
import { toast } from '#lib/shell/notify.svelte.js';

// The gateway's route policy, what a route or slot falls back to
class Defaults {
	policy = $state<Policy | undefined>(undefined);
	#asked = false;

	// Asks the daemon once per session
	load(): void {
		if (this.#asked) return;
		this.#asked = true;
		void rpc.gateway
			.getGatewayStatus({})
			.then((r) => (this.policy = r.status?.policy))
			.catch((err) => {
				this.#asked = false;
				toast.error('Gateway defaults failed to load', reason(err));
			});
	}
}

export const defaults = new Defaults();
