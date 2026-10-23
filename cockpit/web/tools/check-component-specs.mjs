// CLI entry: node tools/check-component-specs.mjs, run from cockpit/web.
import { main } from './lib/component-specs.mjs'

process.exit(main(process.cwd(), console.log))
