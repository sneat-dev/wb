import { describe, expect, it } from 'vitest'
import { assertJourneyHost } from './journey-guard.mjs'

describe('assertJourneyHost', () => {
  it('accepts only Linux on GitHub Actions', () => {
    expect(() => assertJourneyHost('linux', { GITHUB_ACTIONS: 'true' })).not.toThrow()
    expect(() => assertJourneyHost('darwin', { GITHUB_ACTIONS: 'true' })).toThrow('launchd')
    expect(() => assertJourneyHost('win32', {})).toThrow('refusing')
    expect(() => assertJourneyHost('linux', {})).toThrow('outside GitHub Actions')
    expect(() => assertJourneyHost('linux', { GITHUB_ACTIONS: 'false' })).toThrow('outside GitHub Actions')
  })
})
