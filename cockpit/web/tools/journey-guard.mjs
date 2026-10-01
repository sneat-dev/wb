// CLI entry: node tools/journey-guard.mjs, run before the journey builds anything.
import { assertJourneyHost } from './lib/journey-guard.mjs'

try {
  assertJourneyHost(process.platform, process.env)
} catch (error) {
  console.error(error.message)
  process.exit(1)
}
