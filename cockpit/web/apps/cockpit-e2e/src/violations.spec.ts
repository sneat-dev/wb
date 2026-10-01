import { expect, it } from 'vitest'
import { otherConsoleErrors, unexplainedViolations } from './violations'

const attr = { directive: 'style-src-attr', blockedURI: 'inline' }

it('sets aside a violation attributed to the licence banner', () => {
  expect(unexplainedViolations([{ ...attr, inLicenseBanner: true }])).toEqual([])
})

it('keeps the same directive from another element', () => {
  expect(unexplainedViolations([{ ...attr, inLicenseBanner: true }, { ...attr, inLicenseBanner: false }])).toEqual([
    'CSP violation: style-src-attr inline',
  ])
})

it('keeps every violation when no banner is present, and none when nothing was reported', () => {
  expect(unexplainedViolations([{ directive: 'script-src-elem', blockedURI: 'https://x', inLicenseBanner: false }])).toEqual([
    'CSP violation: script-src-elem https://x',
  ])
  expect(unexplainedViolations([])).toEqual([])
})

it('drops the browser console echo of a violation and keeps other errors', () => {
  expect(
    otherConsoleErrors([
      "Applying inline style violates the following Content Security Policy directive 'style-src'",
      'Uncaught TypeError',
    ]),
  ).toEqual(['Uncaught TypeError'])
})
