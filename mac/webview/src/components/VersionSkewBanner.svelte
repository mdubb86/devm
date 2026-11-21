<script lang="ts">
  import type { ProjectStore } from '../lib/store.svelte';

  interface Props {
    store: ProjectStore;
  }
  let { store }: Props = $props();
</script>

{#if store.versionSkew}
  <div class="bg-orange-100 border-b border-orange-200 px-6 py-2 text-sm text-orange-900">
    Daemon fingerprint <code class="font-mono">{store.daemonFingerprint}</code>
    does not match app fingerprint <code class="font-mono">{store.appFingerprint}</code>.
    Run <code class="bg-orange-200 px-1 rounded">devm install</code> to update.
  </div>
{/if}
