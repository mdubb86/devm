<script lang="ts">
  import type { ProjectStore } from '../lib/store.svelte';
  import StatusRow from './StatusRow.svelte';

  interface Props {
    store: ProjectStore;
  }
  let { store }: Props = $props();
</script>

<div class="p-6">
  {#if !store.daemonReachable}
    <div class="rounded-md border border-red-200 bg-red-50 p-4">
      <div class="font-semibold text-red-800">devm daemon not reachable</div>
      <div class="text-sm text-red-700 mt-1">Try <code class="bg-red-100 px-1 rounded">devm service restart</code>.</div>
    </div>
  {:else if store.projects.length === 0}
    <div class="text-gray-500">
      No projects yet. Open a project directory in your terminal and run <code class="bg-gray-100 px-1 rounded">devm start</code>.
    </div>
  {:else}
    <table class="w-full">
      <thead>
        <tr class="text-left text-xs uppercase text-gray-500 border-b border-gray-200">
          <th class="pb-2 pr-4">Project</th>
          <th class="pb-2 pr-4">VM</th>
          <th class="pb-2 pr-4">Path</th>
          <th class="pb-2 pr-4">Iron-proxy</th>
          <th class="pb-2 pr-4">Approve</th>
        </tr>
      </thead>
      <tbody>
        {#each store.projects as project (project.name)}
          <StatusRow {project} />
        {/each}
      </tbody>
    </table>
  {/if}
</div>
