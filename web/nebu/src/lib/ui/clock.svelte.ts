// A shared second hand so relative times and uptimes move without a timer per row
class ClockTick {
	now = $state(Date.now());
	constructor() {
		setInterval(() => (this.now = Date.now()), 1000);
	}
}

export const clockTick = new ClockTick();
