<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { ProjectStore } from './lib/store.svelte';
  import StatusScreen from './components/StatusScreen.svelte';
  import VersionSkewBanner from './components/VersionSkewBanner.svelte';

  const store = new ProjectStore();
  onMount(() => store.start());
  onDestroy(() => store.stop());
</script>

<VersionSkewBanner {store} />
<StatusScreen {store} />
