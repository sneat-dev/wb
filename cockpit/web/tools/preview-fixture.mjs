// CLI entry: COCKPIT_PREVIEW_PORT=4347 node tools/preview-fixture.mjs, run from
// cockpit/web after `pnpm build`. Serves the production build with the library's
// fixtures as the daemon's API, on a loopback port, so the Cockpit can be looked
// at without a daemon. COCKPIT_PREVIEW_STATE (ok, warming, daemon-older,
// page-older) and COCKPIT_PREVIEW_SESSION (anonymous, owner) choose what it shows.
import { join } from 'node:path'
import { loadFixture } from './fixtures.mjs'
import { STATES, previewPort, startPreview } from './lib/preview-fixture.mjs'

const port = previewPort(process.env)
const state = process.env.COCKPIT_PREVIEW_STATE ?? 'ok'
if (!STATES.includes(state)) throw new Error(`preview: COCKPIT_PREVIEW_STATE must be one of ${STATES.join(', ')}`)
const data = await loadFixture()
await startPreview({ distRoot: join(process.cwd(), 'dist'), data, state, session: process.env.COCKPIT_PREVIEW_SESSION ?? 'anonymous' }, port)
console.log(`Cockpit preview (${state}) on http://127.0.0.1:${port}/cockpit/ with the fleet-data fixtures; no daemon is involved`)
