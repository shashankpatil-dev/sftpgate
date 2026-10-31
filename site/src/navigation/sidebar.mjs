// The docs menu. Add a page here, or it won't appear in the left bar.
export const sidebar = [
	{
		label: 'Get started',
		items: [
			'get-started/introduction',
			'get-started/quickstart',
			'get-started/concepts',
			'get-started/built-in-and-plug-in',
		],
	},
	{
		label: 'Deploy',
		items: [
			'deploy/docker',
			'deploy/kubernetes',
			'deploy/bucket-access',
			'deploy/secrets',
			'deploy/config-from-git',
		],
	},
	{
		label: 'Guides',
		items: [
			'guides/add-a-partner',
			'guides/check-files',
			'guides/pgp',
			'guides/copy-files',
			'guides/webhooks',
			'guides/pull-from-a-partner',
			'guides/push-to-a-partner',
			'guides/alerts-to-slack',
			'guides/upgrade',
			{
				label: 'Worked examples',
				items: ['guides/example-send-to-an-auditor', 'guides/example-receive-from-gateways'],
			},
		],
	},
	{
		label: 'Config reference',
		items: [
			'reference/overview',
			'reference/server',
			'reference/http',
			'reference/storages',
			'reference/accounts',
			'reference/folders',
			'reference/actions',
			'reference/hooks',
			'reference/remotes',
			'reference/transfers',
			'reference/complete-example',
			'reference/environment',
			'reference/helm-values',
			'reference/cli',
		],
	},
	{
		label: 'Operate',
		items: [
			'operate/api',
			'operate/audit-log',
			'operate/metrics',
			'operate/alerts',
			'operate/tracing',
			'operate/sizing',
			'operate/sftp-clients',
			'operate/troubleshooting',
		],
	},
	{
		label: 'Project',
		items: [
			'project/glossary',
			'project/security',
			'project/changelog',
		],
	},
];
