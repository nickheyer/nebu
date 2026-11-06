import { ArtifactRole, type Artifact, type Model } from '#lib/proto/nebu/v1/model_pb.js';

export function repoPath(sourceId: string, repo: string): string {
	return `/discover/${encodeURIComponent(sourceId)}/${encodeURIComponent(repo)}`;
}

// The last search the Discover page showed, so a repository page links back to it
export const lastSearch = { href: '/discover' };

// Roles a pull downloads beside a group's weights
const supporting = new Set([ArtifactRole.CONFIG, ArtifactRole.INDEX, ArtifactRole.TOKENIZER, ArtifactRole.TEMPLATE, ArtifactRole.CODE, ArtifactRole.PROJECTOR]);

// The deepest declared pipeline holding a file, else its directory, as the daemon attaches files to weights
function attachKey(model: Model, a: Artifact): string {
	const tree = model.trees.find((t) => t.root === '' || a.path.startsWith(`${t.root}/`));
	if (tree) return `\u0000${tree.root}`;
	const i = a.path.lastIndexOf('/');
	return i < 0 ? '.' : a.path.slice(0, i);
}

export type WeightGroup = { name: string; formatId: string; bytes: number };

// Weight groups of a resolved repository in the daemon's order, format then name
export function weightGroups(model: Model | undefined): WeightGroup[] {
	const byName = new Map<string, WeightGroup>();
	for (const a of model?.artifacts ?? []) {
		if (a.role !== ArtifactRole.WEIGHTS) continue;
		const g = byName.get(a.group) ?? { name: a.group, formatId: a.formatId, bytes: 0 };
		g.bytes += Number(a.sizeBytes);
		byName.set(a.group, g);
	}
	return [...byName.values()].sort((x, y) => x.formatId.localeCompare(y.formatId) || x.name.localeCompare(y.name));
}

// The files a pull of one group downloads, its weights by shard then its config, tokenizer and other supporting files
export function groupFiles(model: Model | undefined, group: string): Artifact[] {
	if (!model) return [];
	const weights = model.artifacts.filter((a) => a.role === ArtifactRole.WEIGHTS && a.group === group).sort((x, y) => x.shardIndex - y.shardIndex || x.path.localeCompare(y.path));
	const first = weights[0];
	if (!first) return [];
	const key = attachKey(model, first);
	const files = model.artifacts.filter((a) => supporting.has(a.role) && a.formatId === first.formatId && attachKey(model, a) === key).sort((x, y) => x.path.localeCompare(y.path));
	return [...weights, ...files];
}
