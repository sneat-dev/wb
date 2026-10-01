import { resolve } from 'node:path'

// The workspace's library entry points, as tsconfig.base.json maps them for the
// Angular build; Vitest does not read those paths, so it is given the same map.
// The more specific name comes first.
export const workspaceAliases = [
  { find: '@cockpit/fleet-data/testing', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/testing.ts') },
  { find: '@cockpit/fleet-data', replacement: resolve(import.meta.dirname, 'libs/fleet-data/src/index.ts') },
  { find: '@cockpit/ui', replacement: resolve(import.meta.dirname, 'libs/ui/src/index.ts') },
]
