<script lang="ts">
import { onDestroy, onMount } from 'svelte';
import { PUBLIC_SERVER_URL } from '$env/static/public';

const TOKEN_STORAGE_KEY = 'viewer_jwt';
const pollEveryMs = 60 * 1000;

let token = $state('');
let screenshotUrl = $state('');
let errorMessage = $state('');
let lastUpdated = $state('');
let loading = $state(false);
let pollId: ReturnType<typeof setInterval> | undefined;

function clearAuth() {
localStorage.removeItem(TOKEN_STORAGE_KEY);
token = '';
if (pollId) {
	clearInterval(pollId);
	pollId = undefined;
}
}

async function fetchScreenshot() {
if (!token) {
errorMessage = 'No token found. Please scan a QR code first.';
return;
}

loading = true;
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

const imageBlob = await response.blob();
if (screenshotUrl) {
URL.revokeObjectURL(screenshotUrl);
}
screenshotUrl = URL.createObjectURL(imageBlob);
lastUpdated = new Date().toLocaleString();
} catch (error) {
errorMessage = error instanceof Error ? error.message : 'failed to fetch screenshot';
} finally {
loading = false;
}
}

onMount(() => {
token = localStorage.getItem(TOKEN_STORAGE_KEY) ?? '';
if (token) {
fetchScreenshot();
pollId = setInterval(fetchScreenshot, pollEveryMs);
} else {
errorMessage = 'No token found. Please scan a QR code first.';
}
});

onDestroy(() => {
if (pollId) {
clearInterval(pollId);
}
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

<button onclick={fetchScreenshot} disabled={loading || !token}>
{loading ? 'Refreshing…' : 'Refresh now'}
</button>
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

button {
margin-top: 1rem;
padding: 0.6rem 1rem;
}

.error {
color: #b00020;
}
</style>
