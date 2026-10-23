import { expect, it } from 'vitest'
import { otherConsoleErrors, unexplainedViolations } from './violations'

it('reports every violation, and none when nothing was reported', () => {
  expect(unexplainedViolations([{ directive: 'style-src-attr', blockedURI: 'inline' }, { directive: 'script-src-elem', blockedURI: 'https://x' }])).toEqual([
    'CSP violation: style-src-attr inline',
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
