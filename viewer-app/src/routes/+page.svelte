<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { PUBLIC_SERVER_URL } from '$env/static/public';

	const TOKEN_STORAGE_KEY = 'viewer_jwt';
	const pollEveryMs = 60 * 1000;

	let token = $state('');
	let screenshotUrl = $state('');
	let errorMessage = $state('');
	let lastUpdated = $state('');
	let pollId: ReturnType<typeof setInterval> | undefined;

	type ScreenshotFailureResponse = {
		status: string;
		message?: string;
	};

	function stopPolling() {
		if (pollId) {
			clearInterval(pollId);
			pollId = undefined;
		}
	}

	function startPolling() {
		stopPolling();
		pollId = setInterval(fetchScreenshot, pollEveryMs);
	}

	function clearAuth() {
		localStorage.removeItem(TOKEN_STORAGE_KEY);
		token = '';
		stopPolling();
	}

	async function fetchScreenshot() {
		if (!token) {
			errorMessage = 'No token found. Please scan a QR code first.';
			return;
		}

		errorMessage = '';
		try {
			const response = await fetch(`${PUBLIC_SERVER_URL}/api/screenshot`, {
				headers: {
					Authorization: ['Bearer', token].join(' ')
				}
			});

			if (response.status === 401) {
				clearAuth();
				errorMessage = 'Session expired or invalid. Please scan again.';
				return;
			}

			if (!response.ok) {
				throw new Error(`request failed (${response.status})`);
			}

			const contentType = response.headers.get('content-type') ?? '';
			if (contentType.includes('application/json')) {
				const payload = (await response.json()) as ScreenshotFailureResponse;
				if (payload.status === 'validation_failed') {
					if (screenshotUrl) {
						URL.revokeObjectURL(screenshotUrl);
					}
					screenshotUrl = '';
					lastUpdated = new Date().toLocaleString();
					errorMessage =
						payload.message ??
						'Screenshot was rejected by validator. A new capture will be requested automatically.';
					return;
				}
				throw new Error('unexpected screenshot response');
			}

			const imageBlob = await response.blob();
			if (screenshotUrl) {
				URL.revokeObjectURL(screenshotUrl);
			}
			screenshotUrl = URL.createObjectURL(imageBlob);
			lastUpdated = new Date().toLocaleString();
		} catch (error) {
			errorMessage = error instanceof Error ? error.message : 'failed to fetch screenshot';
		}
	}

	onMount(() => {
		token = localStorage.getItem(TOKEN_STORAGE_KEY) ?? '';
		const handleVisibilityChange = () => {
			if (document.hidden) {
				stopPolling();
				return;
			}
			if (token) {
				fetchScreenshot();
				startPolling();
			}
		};

		if (token) {
			fetchScreenshot();
			startPolling();
		} else {
			errorMessage = 'No token found. Please scan a QR code first.';
		}

		document.addEventListener('visibilitychange', handleVisibilityChange);
		return () => {
			document.removeEventListener('visibilitychange', handleVisibilityChange);
		};
	});

	onDestroy(() => {
		stopPolling();
		if (screenshotUrl) {
			URL.revokeObjectURL(screenshotUrl);
		}
	});
</script>

<svelte:head>
<title>Remote Screenshot Viewer</title>
</svelte:head>

<main>
	<h1>Remote Screenshot Viewer</h1>
	<p><a href="/scanner">Scan token</a></p>

	{#if errorMessage}
		<p class="error">{errorMessage}</p>
	{/if}

	{#if screenshotUrl}
		<img src={screenshotUrl} alt="Latest remote screenshot" />
		<p><strong>Last Updated:</strong> {lastUpdated}</p>
	{/if}
</main>

<style>
	main {
		max-width: 900px;
		margin: 2rem auto;
		padding: 1rem;
		font-family: system-ui, sans-serif;
	}

	img {
		width: 100%;
		height: auto;
		border: 1px solid #ddd;
		background: #fff;
	}

	.error {
		color: #b00020;
	}
</style>
