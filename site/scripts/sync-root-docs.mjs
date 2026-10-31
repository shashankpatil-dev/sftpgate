// CHANGELOG.md and SECURITY.md stay at the repository root, where GitHub
// expects them. This copies them into the docs so they exist in one place only.
import { readFile, writeFile } from 'node:fs/promises';

const pages = [
	{
		from: '../CHANGELOG.md',
		to: 'src/content/docs/project/changelog.md',
		title: 'Changelog',
		description: 'What changed in each version, and how to upgrade.',
	},
	{
		from: '../SECURITY.md',
		to: 'src/content/docs/project/security.md',
		title: 'Security',
		description: 'How to report a vulnerability, and what is in scope.',
	},
];

// Links in these files are written for GitHub, so they point at the repository.
const repo = 'https://github.com/shashankpatil-dev/sftpgate/blob/main/';

for (const { from, to, title, description } of pages) {
	const source = new URL(`../${from}`, import.meta.url);
	const body = (await readFile(source, 'utf8'))
		.replace(/^# .*\n/, '')
		.replace(/\]\((?!https?:|#)([^)]+)\)/g, `](${repo}$1)`);
	const page = `---\ntitle: ${title}\ndescription: ${description}\neditUrl: false\n---\n${body}`;
	await writeFile(new URL(`../${to}`, import.meta.url), page);
}
