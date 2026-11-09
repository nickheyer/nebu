import { ArtifactRole, type Model } from '#lib/proto/nebu/v1/model_pb.js';

export function repoPath(sourceId: string, repo: string): string {
	return `/discover/${encodeURIComponent(sourceId)}/${encodeURIComponent(repo)}`;
}

const short = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });

// A large count as 1.2M or 48K
export function compact(n: bigint | number): string {
	return short.format(Number(n));
}

// The last search the Discover page showed, so a repository page links back to it
export const lastSearch = { href: '/discover' };

export type WeightGroup = { name: string; formatId: string; bytes: number; files: number };

// Weight groups of a resolved repository in the daemon's order, format then name
export function weightGroups(model: Model | undefined): WeightGroup[] {
	const byName = new Map<string, WeightGroup>();
	for (const a of model?.artifacts ?? []) {
		if (a.role !== ArtifactRole.WEIGHTS) continue;
		const g = byName.get(a.group) ?? { name: a.group, formatId: a.formatId, bytes: 0, files: 0 };
		g.bytes += Number(a.sizeBytes);
		g.files += 1;
		byName.set(a.group, g);
	}
	return [...byName.values()].sort((x, y) => x.formatId.localeCompare(y.formatId) || x.name.localeCompare(y.name));
}

