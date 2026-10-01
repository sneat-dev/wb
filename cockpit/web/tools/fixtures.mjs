// Loads the fleet-data fixtures for the preview harness (tools/preview-fixture.mjs,
// tools/shots.mjs). They are TypeScript in libs/fleet-data, so Vite loads them
// the way the unit tests do, with the workspace's aliases; nothing is built.
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer } from 'vite'
import { workspaceAliases } from '../vite.aliases.mts'

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

// {document, metrics, branches}: performanceFixture(), 500 repositories, 600 worktrees and 3 machines;
// with `home` the hand-made fleets Home is photographed against (tools/home-states.ts), by name.
export async function loadFixture({ home = false } = {}) {
  const vite = await createServer({
    root: webRoot,
    configFile: false,
    logLevel: 'error',
    appType: 'custom',
    server: { middlewareMode: true, hmr: false, watch: null },
    optimizeDeps: { noDiscovery: true },
    resolve: { alias: workspaceAliases },
  })
  try {
    const { performanceFixture } = await vite.ssrLoadModule('@cockpit/fleet-data/testing')
    if (!home) return performanceFixture()
    const { homeStates } = await vite.ssrLoadModule('/tools/home-states.ts')
    return { ...performanceFixture(), home: homeStates() }
  } finally {
    await vite.close()
  }
}
