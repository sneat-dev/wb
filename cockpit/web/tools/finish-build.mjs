// CLI entry: node tools/finish-build.mjs, run from cockpit/web after the build.
import { finishBuild } from './lib/finish-build.mjs'

process.exit(finishBuild('dist', console.error, console.log))
