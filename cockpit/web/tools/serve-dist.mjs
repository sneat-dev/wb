// CLI entry: PORT=4300 node tools/serve-dist.mjs, run from cockpit/web.
import { createServer } from 'node:http'
import { join } from 'node:path'
import { createHandler } from './lib/serve-dist.mjs'

createServer(createHandler(join(process.cwd(), 'dist'))).listen(Number(process.env.PORT ?? 4300), '127.0.0.1')
