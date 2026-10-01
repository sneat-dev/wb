import { resolve } from 'node:path'

// The workspace's library entry points, as tsconfig.base.json maps them for the
// Angular build; Vitest does not read those paths, so it is given the same map.
// The more specific name comes first. A page that needs one component imports its own
// entry (`@cockpit/ui/count`), not the barrel, so it does not pull in the PrimeNG ones.
export const workspaceAliases = [
  { find: '@cockpit/fleet-data/home-details', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/home-details.ts') },
  { find: '@cockpit/fleet-data/panel', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/panel.ts') },
  { find: '@cockpit/fleet-data/task-reason', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/task-reason.ts') },
  { find: '@cockpit/fleet-data/commands', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/commands.ts') },
  { find: '@cockpit/fleet-data/list', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/list.ts') },
  { find: '@cockpit/fleet-data/lazy-client', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/lazy-client.ts') },
  { find: '@cockpit/fleet-data/testing', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/testing.ts') },
  { find: '@cockpit/fleet-data', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/index.ts') },
  { find: '@cockpit/ui/count', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/count/count.ts') },
  { find: '@cockpit/ui/route-label', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/route-label/route-label.ts') },
  { find: '@cockpit/ui/state', replacement: resolve(import.meta.dirname, 'libs/ui/src/state.ts') },
  { find: '@cockpit/ui/code-index-label', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/code-index-label/code-index-label.ts') },
  { find: '@cockpit/ui/code-index-panel', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/code-index-panel/code-index-panel.ts') },
  { find: '@cockpit/ui/list-host', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/list/list-host.ts') },
  { find: '@cockpit/ui/list', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/list/index.ts') },
  { find: '@cockpit/ui/panel', replacement: resolve(import.meta.dirname, 'libs/ui/src/lib/panel/index.ts') },
  { find: '@cockpit/ui/control', replacement: resolve(import.meta.dirname, 'libs/ui/src/control.ts') },
  { find: '@cockpit/ui/chart', replacement: resolve(import.meta.dirname, 'libs/ui/src/chart.ts') },
  { find: '@cockpit/ui', replacement: resolve(import.meta.dirname, 'libs/ui/src/index.ts') },
]
