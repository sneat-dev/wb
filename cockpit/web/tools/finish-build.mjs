// CLI entry: node tools/finish-build.mjs, run from cockpit/web after the build.
import { readFileSync } from 'node:fs'
import { finishBuild } from './lib/finish-build.mjs'

process.exit(finishBuild('dist', console.error, console.log, readFileSync('apps/cockpit/src/app/app.routes.ts', 'utf8')))
