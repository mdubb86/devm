<script lang="ts">
  import type { ProjectStatus } from '../lib/types';

  interface Props {
    project: ProjectStatus;
  }
  let { project }: Props = $props();

  function vmDot(): string {
    if (project.orphaned) return 'bg-red-500';
    if (project.vm_running) return 'bg-green-500';
    return 'bg-gray-400';
  }

  function vmLabel(): string {
    if (project.orphaned) return 'orphaned';
    return project.vm_running ? 'running' : 'stopped';
  }

  function proxyClass(): string {
    switch (project.proxy.status) {
      case 'ok': return 'text-green-700';
      case 'missing': return 'text-red-700';
      case 'stale': return 'text-orange-700';
      default: return 'text-gray-700';
    }
  }

  function shortenPath(p: string | undefined): string {
    if (!p) return '';
    const home = '/Users/';
    if (p.startsWith(home)) {
      const rest = p.slice(home.length);
      const slash = rest.indexOf('/');
      if (slash >= 0) return '~' + rest.slice(slash);
    }
    return p;
  }
</script>

<tr class="border-b border-gray-100">
  <td class="py-2 pr-4 font-mono text-sm">{project.name}</td>
  <td class="py-2 pr-4">
    <span class="inline-flex items-center gap-2">
      <span class="w-2 h-2 rounded-full {vmDot()}"></span>
      {vmLabel()}
    </span>
  </td>
  <td class="py-2 pr-4 text-sm text-gray-600">{shortenPath(project.mac_cwd)}</td>
  <td class="py-2 pr-4 font-medium {proxyClass()}">{project.proxy.status}</td>
  <td class="py-2 pr-4">
    {#if project.approve_state?.diverged}
      <span class="text-orange-700">diverged</span>
    {:else if project.approve_state}
      <span class="text-green-700">up-to-date</span>
    {:else}
      <span class="text-gray-400">—</span>
    {/if}
  </td>
</tr>
