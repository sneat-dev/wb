// Runs after the Angular build. The build empties dist/, which removes the
// tracked dist/.gitkeep placeholder that lets go:embed compile in a clean
// clone, so put it back. It fails when the build emitted no entry document
// (cockpit/web/embed.go treats dist/index.html as the "built" marker, so
// without it wb would embed a placeholder and serve the "not built" page) or
// when that document lost the __CSP_NONCE__ placeholder, which embed.go
// replaces with the per-response style nonce.
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export const NONCE_PLACEHOLDER = '__CSP_NONCE__'

export function finishBuild(dist, log) {
  const index = join(dist, 'index.html')
  if (!existsSync(index)) {
    log('cockpit/web build emitted no dist/index.html')
    return 1
  }
  if (!readFileSync(index, 'utf8').includes(NONCE_PLACEHOLDER)) {
    log(`cockpit/web dist/index.html does not contain the ${NONCE_PLACEHOLDER} placeholder, so the daemon cannot issue a style nonce`)
    return 1
  }
  writeFileSync(join(dist, '.gitkeep'), '')
  return 0
}
