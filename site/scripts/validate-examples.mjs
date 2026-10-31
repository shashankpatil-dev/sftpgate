// Runs `sftpgate validate -offline` on every complete config in the docs, so
// an example can't go out of date unnoticed. A complete config is a yaml code
// block titled "sftpgate.yaml"; shorter snippets aren't valid on their own.
//
// Usage: node scripts/validate-examples.mjs [path/to/sftpgate]
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const binary = process.argv[2] ?? '../bin/sftpgate';
const docs = 'src/content/docs';
const block = /```yaml title="sftpgate\.yaml"\n([\s\S]*?)```/g;

const dir = mkdtempSync(join(tmpdir(), 'sftpgate-examples-'));
let checked = 0;
let failed = 0;

for (const entry of readdirSync(docs, { recursive: true })) {
	if (!/\.mdx?$/.test(entry)) continue;
	const text = readFileSync(join(docs, entry), 'utf8');
	for (const [, yaml] of text.matchAll(block)) {
		const file = join(dir, `${checked}.yaml`);
		writeFileSync(file, yaml);
		checked++;
		try {
			execFileSync(binary, ['validate', '-offline', file], { stdio: 'pipe' });
		} catch (err) {
			failed++;
			console.error(`${entry}:\n${err.stderr}`);
		}
	}
}

rmSync(dir, { recursive: true });
console.log(`${checked - failed} of ${checked} examples are valid`);
process.exit(failed ? 1 : 0);
