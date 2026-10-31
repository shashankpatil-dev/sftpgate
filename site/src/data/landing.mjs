// The words on the landing page. Change copy here, not in the components.

export const pullCommand = 'docker pull ghcr.io/shashankpatil-dev/sftpgate:0.4.0';

export const links = {
	quickstart: 'get-started/quickstart/',
	docs: 'get-started/introduction/',
	github: 'https://github.com/shashankpatil-dev/sftpgate',
	builtIn: 'get-started/built-in-and-plug-in/',
};

// The hero: a real account, as a reviewer would see it in a pull request.
export const diff = [
	{ kind: 'same', text: 'accounts:' },
	{ kind: 'add', text: '  - name: paygate' },
	{ kind: 'add', text: '    auth:' },
	{ kind: 'add', text: '      public_keys: ["ssh-ed25519 AAAA... paygate"]' },
	{ kind: 'add', text: '    allow_ips: ["52.66.0.0/16"]' },
	{ kind: 'add', text: '    folders:' },
	{ kind: 'add', text: '      - path: /inbound' },
	{ kind: 'add', text: '        storage: recon-raw' },
	{ kind: 'add', text: '        prefix: paygate/' },
	{ kind: 'add', text: '        perms: [list, upload]' },
	{ kind: 'add', text: '        patterns: ["*.csv"]' },
];

export const about = {
	title: 'What is SFTPgate?',
	text: 'A secure front door for exchanging files with partners. They connect with any SFTP client, SFTPgate checks who they are and what they may do, and their files go straight into your own cloud bucket.',
};

export const highlights = [
	{
		title: 'One YAML file in git',
		text: 'Every access change has an author, a reviewer and a history.',
	},
	{
		title: 'Complete files only',
		text: 'A file reaches the bucket once it’s finished, with its checksum verified.',
	},
	{
		title: 'Nothing extra to run',
		text: 'One open-source container under Apache-2.0, with no database.',
	},
];

// A real sequence, so it is numbered.
export const journey = [
	{
		title: 'At the door',
		text: 'IP allowlist, connection limits and login throttling run before a partner can log in.',
	},
	{
		title: 'Login',
		text: 'A key, a password, or both. Modern encryption only, and no shell or tunnels.',
	},
	{
		title: 'Folder rules',
		text: 'The partner sees only their folders, and can do only what you listed.',
	},
	{
		title: 'Upload finishes',
		text: 'The file waits on disk until it is complete. A dropped connection leaves nothing behind.',
	},
	{
		title: 'Checks you add',
		text: 'A wrong CSV header or size refuses the file, and it is never stored.',
	},
	{
		title: 'Saved and verified',
		text: 'The bucket gets the file. Size and SHA-256 are read back before the partner hears “done”.',
	},
];

export const builtIn = [
	'Complete files only, with SHA-256 on every file',
	'Folder jail and per-folder permissions',
	'No overwrites unless you allow them',
	'An audit line for every login and file operation',
	'Connection limits and blocking of repeat login failures',
	'Config reload without a restart; a broken file is refused',
	'Prometheus metrics and health checks',
];

export const plugIn = [
	'Webhooks, signed, on upload, download, push and pull',
	'PGP decrypt and encrypt',
	'CSV and size checks, before a file is stored or sent',
	'Copies to another bucket, with names you choose',
	'Scheduled pulls from and pushes to partner servers',
	'A read-only JSON API, and two admin calls',
	'OpenTelemetry traces',
];

// Real config, shown beside what it does.
export const showcase = [
	{
		title: 'Refuse a bad file at the door',
		text: 'A wrong header or an empty report is refused while the partner is connected, and never reaches your bucket.',
		link: { label: 'Check files', href: 'guides/check-files/' },
		lang: 'yaml',
		code: `actions:
  settlement-csv:
    check:
      min_size: 100
      csv:
        header: [settlement_id, amount]
        min_rows: 1

# in the partner's folder
hooks:
  before_upload: [settlement-csv]`,
	},
	{
		title: 'Tell your systems when a file lands',
		text: 'Decrypt it, copy it to your warehouse, then send a signed webhook. Failed steps retry on their own and survive restarts.',
		link: { label: 'Webhooks', href: 'guides/webhooks/' },
		lang: 'yaml',
		code: `actions:
  warehouse:
    copy: { storage: warehouse, key: "{account}/{date}/{filename}" }
  notify:
    webhook:
      url: "https://recon.internal/hooks/sftpgate"
      secret: "\${env:HOOK_SECRET}"

# in the partner's folder
hooks:
  after_upload: [decrypt, warehouse, notify]`,
	},
	{
		title: 'Send and fetch files on a schedule',
		text: 'Your service writes to a bucket, and SFTPgate delivers. There are no SFTP scripts or cron jobs to maintain.',
		link: { label: 'Push to a partner', href: 'guides/push-to-a-partner/' },
		lang: 'yaml',
		code: `transfers:
  - name: bank-payouts
    schedule: "*/30 * * * *"
    push:
      from: { storage: reports, prefix: outbox/ }
      to: { remote: bank, path: /in }
      after: "move:sent/"`,
	},
	{
		title: 'Know who did what',
		text: 'Every login and file operation is one JSON line, ready for your log system and your auditors.',
		link: { label: 'Audit log', href: 'operate/audit-log/' },
		lang: 'json',
		code: `{
  "event": "upload",
  "result": "ok",
  "account": "paygate",
  "ip": "52.66.10.4",
  "key": "paygate/settlement.csv",
  "sha256": "5891b5b5..."
}`,
	},
];
