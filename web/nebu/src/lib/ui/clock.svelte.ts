// A shared second hand for relative times
class ClockTick {
	now = $state(Date.now());
	constructor() {
		setInterval(() => (this.now = Date.now()), 1000);
	}
}

export const clockTick = new ClockTick();
