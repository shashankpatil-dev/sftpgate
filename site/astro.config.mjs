import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { sidebar } from './src/navigation/sidebar.mjs';

export default defineConfig({
	site: 'https://shashankpatil-dev.github.io',
	base: '/sftpgate',
	devToolbar: { enabled: false },
	integrations: [
		starlight({
			title: 'SFTPgate',
			description: 'An SFTP gateway to your cloud buckets, configured from one YAML file in git.',
			logo: { src: './src/assets/logo.svg', alt: 'SFTPgate' },
			favicon: '/favicon.svg',
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/shashankpatil-dev/sftpgate' },
			],
			editLink: {
				baseUrl: 'https://github.com/shashankpatil-dev/sftpgate/edit/main/site/',
			},
			lastUpdated: false,
			tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
			sidebar,
			components: {
				ThemeProvider: './src/components/docs/Empty.astro',
				ThemeSelect: './src/components/docs/Empty.astro',
			},
			customCss: [
				'@fontsource-variable/schibsted-grotesk',
				'@fontsource/ibm-plex-mono/400.css',
				'@fontsource/ibm-plex-mono/500.css',
				'./src/styles/tokens.css',
				'./src/styles/docs.css',
			],
			expressiveCode: {
				themes: ['github-dark-default'],
				styleOverrides: {
					borderRadius: '6px',
					borderColor: '#2e323a',
					codeBackground: '#0f1114',
					codeFontSize: '0.84rem',
					frames: { frameBoxShadowCssValue: 'none' },
				},
			},
		}),
	],
});
