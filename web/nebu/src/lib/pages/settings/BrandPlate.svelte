<script lang="ts">
	import Card from '#lib/ui/layout/Card.svelte';
	import GithubLogoIcon from 'phosphor-svelte/lib/GithubLogoIcon';

	// A groove is a dark hairline with its lit edge a step down and right
	const grooves = [
		{ tone: 'light', shift: 0.4 },
		{ tone: 'dark', shift: 0 }
	];
</script>

<Card>
	<div class="plate">
		<svg class="art" viewBox="0 0 320 160" role="img" aria-label="nebu">
			<defs>
				{#each grooves as g (g.tone)}
					<linearGradient id="plate-across-{g.tone}"><stop class={g.tone} offset="0" stop-opacity="0" /><stop class={g.tone} offset="0.5" /><stop class={g.tone} offset="1" stop-opacity="0" /></linearGradient>
					<linearGradient id="plate-down-{g.tone}" x2="0" y2="1"><stop class={g.tone} offset="0" stop-opacity="0" /><stop class={g.tone} offset="0.4" /><stop class={g.tone} offset="1" stop-opacity="0" /></linearGradient>
				{/each}
				<linearGradient id="plate-slash" gradientUnits="userSpaceOnUse" x2="0" y2="112"><stop class="lit" offset="0" stop-opacity="0" /><stop class="lit" offset="0.14" /><stop class="lit" offset="0.9" /><stop class="lit" offset="1" stop-opacity="0" /></linearGradient>
				<filter id="plate-well" x="-20%" y="-20%" width="140%" height="140%" color-interpolation-filters="sRGB">
					<feComponentTransfer in="SourceAlpha" result="outside"><feFuncA type="table" tableValues="1 0" /></feComponentTransfer>
					<feGaussianBlur in="outside" stdDeviation="0.5" />
					<feOffset dx="-0.8" dy="-0.8" result="lit" />
					<feFlood class="light" />
					<feComposite in2="lit" operator="in" />
					<feComposite in2="SourceAlpha" operator="in" result="rim" />
					<feGaussianBlur in="outside" stdDeviation="1.4" />
					<feOffset dx="1.6" dy="1.6" result="cast" />
					<feFlood class="dark" />
					<feComposite in2="cast" operator="in" />
					<feComposite in2="SourceAlpha" operator="in" result="shade" />
					<feMerge><feMergeNode in="SourceGraphic" /><feMergeNode in="rim" /><feMergeNode in="shade" /></feMerge>
				</filter>
			</defs>
			{#each grooves as g (g.tone)}
				<g transform="translate({g.shift} {g.shift})">
					<rect y="15.8" width="320" height="0.4" fill="url(#plate-across-{g.tone})" />
					<rect y="99.8" width="320" height="0.4" fill="url(#plate-across-{g.tone})" />
					<rect x="117.8" width="0.4" height="160" fill="url(#plate-down-{g.tone})" />
					<rect x="201.8" width="0.4" height="160" fill="url(#plate-down-{g.tone})" />
				</g>
			{/each}
			<g class="edges" stroke="url(#plate-slash)" stroke-width="0.4">
				<line x1="123.92" x2="169.43" y2="112" />
				<line x1="148.95" x2="194.44" y2="112" />
			</g>
			<g class="wells" filter="url(#plate-well)">
				<path transform="translate(118 16) scale(3.5)" d="M0 0H6V4H4V20H6V24H0ZM24 0H18V4H20V20H18V24H24Z" />
			</g>
			<g class="slash">
				<path transform="translate(118 16) scale(3.5)" d="M5.5 0H10.7L18.5 19.2V24H13.3L5.5 4.8Z" />
			</g>
			<g class="word" transform="translate(118 114) scale(0.6885)">
				<path d="M0 0H6V4H4V20H6V24H0ZM24 0H18V4H20V20H18V24H24ZM5.5 0H10.7L18.5 19.2V24H13.3L5.5 4.8Z" />
				<path d="M40 0H54V4H44V10H51V14H44V20H54V24H40Z" />
				<path fill-rule="evenodd" d="M70 0H82L86 4V10L84 12L86 14V20L82 24H70ZM74 4V10H81L82 9V5L81 4ZM74 14V20H81L82 19V15L81 14Z" />
				<path d="M102 0H108V4H106V20H118V4H116V0H122V24H102Z" />
			</g>
		</svg>
		<a class="source" href="https://github.com/nickheyer/nebu" target="_blank" rel="noreferrer" aria-label="GitHub"><GithubLogoIcon size={22} /></a>
	</div>
</Card>

<style>
	/* The brackets are wells pressed into the card and the slash a lit bar raised over them, under the light every control sits in */
	.plate {
		position: relative;
		min-width: 0;
	}
	.art {
		display: block;
		width: 100%;
		height: auto;
		aspect-ratio: 2 / 1;
	}
	stop.dark,
	feFlood.dark {
		stop-color: var(--neo-shadow-color-dark);
		flood-color: var(--neo-shadow-color-dark);
	}
	stop.light,
	feFlood.light {
		stop-color: var(--neo-shadow-color-light);
		flood-color: var(--neo-shadow-color-light);
	}
	stop.lit {
		stop-color: var(--neo-color-primary);
	}
	.edges {
		opacity: 0.45;
	}
	.wells {
		fill: color-mix(in srgb, var(--neo-background-color), var(--neo-shadow-color-dark) 60%);
	}
	.slash {
		fill: var(--neo-color-primary);
		filter: drop-shadow(0.8px 0.8px 1px var(--neo-shadow-color-dark)) drop-shadow(0 0 3px color-mix(in srgb, var(--neo-color-primary) 30%, transparent));
	}
	.word {
		fill: var(--neo-text-color);
	}
	.source {
		position: absolute;
		bottom: 0;
		left: 0;
		display: grid;
		place-items: center;
		width: var(--nebu-control);
		height: var(--nebu-control);
		margin: calc((22px - var(--nebu-control)) / 2);
		color: var(--nebu-text-muted);
		border-radius: var(--neo-border-radius);
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.source:hover,
	.source:focus-visible {
		color: var(--neo-text-color);
	}
	.source:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: -2px;
	}
</style>
