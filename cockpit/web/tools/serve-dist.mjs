// CLI entry: PORT=4300 node tools/serve-dist.mjs, run from cockpit/web. DIST_DIR (default dist)
// names the build to serve: the stubbed end-to-end run also serves dist-preview, the preview build.
import { createServer } from 'node:http'
import { join } from 'node:path'
import { createHandler } from './lib/serve-dist.mjs'

createServer(createHandler(join(process.cwd(), process.env.DIST_DIR ?? 'dist'))).listen(Number(process.env.PORT ?? 4300), '127.0.0.1')
