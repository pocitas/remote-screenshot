<script lang="ts">
import QRCode from 'qrcode';
import { onDestroy, onMount } from 'svelte';
import { PUBLIC_GATE_SECRET, PUBLIC_SERVER_URL } from '$env/static/public';

let token = $state('');
let tokenExpiresAt = $state('');
let qrDataUrl = $state('');
let errorMessage = $state('');
let lastRefreshAt = $state('');
let loading = $state(false);
let intervalId: ReturnType<typeof setInterval> | undefined;

const refreshEveryMs = 5 * 60 * 1000;

async function refreshToken() {
loading = true;
errorMessage = '';
try {
const response = await fetch(`${PUBLIC_SERVER_URL}/api/gate/token`, {
method: 'POST',
headers: {
'X-Gate-Secret': PUBLIC_GATE_SECRET
}
});

if (!response.ok) {
throw new Error(`token request failed (${response.status})`);
}

const payload = await response.json();
token = payload.token ?? '';
tokenExpiresAt = payload.expires_at ?? '';
if (!token) {
throw new Error('server returned empty token');
}
qrDataUrl = await QRCode.toDataURL(token, { width: 320, margin: 1 });
lastRefreshAt = new Date().toLocaleString();
} catch (error) {
errorMessage = error instanceof Error ? error.message : 'failed to refresh token';
} finally {
loading = false;
}
}

onMount(() => {
refreshToken();
intervalId = setInterval(refreshToken, refreshEveryMs);
});

onDestroy(() => {
if (intervalId) {
clearInterval(intervalId);
}
});
</script>

<svelte:head>
<title>Gate Token Screen</title>
</svelte:head>

<main>
<h1>Gate Token</h1>
<p>Refreshes every 5 minutes. Scan this QR from the viewer app.</p>

{#if loading && !qrDataUrl}
<p>Loading token…</p>
{/if}

{#if errorMessage}
<p class="error">{errorMessage}</p>
{/if}

{#if qrDataUrl}
<img alt="JWT QR code" src={qrDataUrl} />
<p><strong>Expires:</strong> {tokenExpiresAt || 'n/a'}</p>
<p><strong>Last refreshed:</strong> {lastRefreshAt || 'n/a'}</p>
{/if}

<button onclick={refreshToken} disabled={loading}>Refresh now</button>
</main>

<style>
main {
max-width: 420px;
margin: 2rem auto;
padding: 1rem;
text-align: center;
font-family: system-ui, sans-serif;
}

img {
width: min(100%, 320px);
height: auto;
border: 1px solid #ddd;
padding: 0.5rem;
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
