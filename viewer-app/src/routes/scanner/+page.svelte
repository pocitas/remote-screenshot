<script lang="ts">
import { goto } from '$app/navigation';
import { onDestroy, onMount } from 'svelte';

const TOKEN_STORAGE_KEY = 'viewer_jwt';
let errorMessage = $state('');
let scannerInstance: { clear: () => Promise<void> } | null = null;

onMount(async () => {
try {
const module = await import('html5-qrcode');
const scanner = new module.Html5QrcodeScanner(
'qr-reader',
{ fps: 5, qrbox: { width: 280, height: 280 } },
false
);
scannerInstance = scanner;

scanner.render(
(decodedText: string) => {
localStorage.setItem(TOKEN_STORAGE_KEY, decodedText.trim());
scanner
.clear()
.catch(() => {})
.finally(() => goto('/'));
},
() => {
// Ignore per-frame decode errors.
}
);
} catch (error) {
errorMessage = error instanceof Error ? error.message : 'Failed to initialize QR scanner';
}
});

onDestroy(async () => {
if (scannerInstance) {
await scannerInstance.clear().catch(() => {});
}
});
</script>

<svelte:head>
<title>Scan Access Token</title>
</svelte:head>

<main>
<h1>Scan access token</h1>
<p>Point your camera at the QR code shown by the gate app.</p>
<div id="qr-reader"></div>
{#if errorMessage}
<p class="error">{errorMessage}</p>
{/if}
</main>

<style>
main {
max-width: 640px;
margin: 2rem auto;
padding: 1rem;
font-family: system-ui, sans-serif;
}

#qr-reader {
margin-top: 1rem;
}

.error {
color: #b00020;
}
</style>
