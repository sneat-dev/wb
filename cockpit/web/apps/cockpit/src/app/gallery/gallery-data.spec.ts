import { isTerminal } from '@cockpit/fleet-data'
import { badgeSpec } from '@cockpit/ui'
import {
  BADGE_ROWS,
  GALLERY_AGE_BUCKETS,
  SYNC_ROWS,
  galleryAnonymousRegistry,
  galleryCommandLists,
  galleryLandedPerDay,
  galleryMachines,
  galleryMetrics,
  galleryPullRequests,
  galleryRegistry,
} from './gallery-data'

const NOW = Date.parse('2026-10-01T12:00:00Z')

describe('the gallery data', () => {
  it('has a row for every state vocabulary, with a value outside it and an absent one', () => {
    expect(BADGE_ROWS.map((row) => row.kind)).toEqual(['task', 'owner', 'agent-activity', 'agent-state', 'pr-state', 'mergeable', 'code-index', 'route', 'load', 'checks'])
    expect(BADGE_ROWS.every((row) => row.values.every((value) => badgeSpec(row.kind, value).label !== ''))).toBe(true)
    expect(SYNC_ROWS).toHaveLength(6)
  })

  it('has pull requests and machines in the states a row can show', () => {
    expect(galleryPullRequests(NOW).map((item) => item.pullRequest.number)).toEqual([128, 131, 140, 141, 99, 150])
    expect(galleryMachines(NOW).map((item) => [item.machine.machine, item.machine.route, item.stale])).toEqual([
      ['alpha', 'local', false],
      ['vm', 'cached', false],
      ['laptop', 'cached', true],
      ['build-box', 'live-remote', false],
      ['hetzner', 'live-remote', false],
    ])
  })

  it('builds its command lists with the library: a refused value has a reason, no list is empty, none has --apply', () => {
    const lists = galleryCommandLists()
    expect(lists.map((list) => list.entries.length > 0)).toEqual([true, true, true, true, true, true])
    const texts = lists.flatMap((list) => list.entries.flatMap((entry) => (entry.command.ok ? [entry.command.text] : [])))
    expect(texts.some((text) => text.includes('--apply'))).toBe(false)
    expect(lists[1].entries[0].command).toMatchObject({ ok: true, label: 'run on vm' })
    expect(lists[5].entries.map((entry) => entry.command.ok)).toEqual([false, false, false, true])
  })

  it('has a registry with every safety class and its anonymous view, nothing permitted', () => {
    expect(galleryRegistry().map((action) => action.safety)).toEqual(['guarded', 'safe', 'safe', 'destructive', 'destructive'])
    expect(galleryAnonymousRegistry().every((action) => !action.permitted)).toBe(true)
    expect(isTerminal('running')).toBe(false)
  })

  it('has an hour of metrics with a missing stretch, fourteen days of landings and the age buckets', () => {
    const { samples } = galleryMetrics(NOW)
    expect(samples).toHaveLength(48)
    expect(Date.parse(samples[samples.length - 1].sampled_at)).toBe(NOW)
    expect(galleryLandedPerDay(NOW)).toHaveLength(14)
    expect(GALLERY_AGE_BUCKETS.map((bucket) => bucket.term)).toEqual(['<1d', '1-7d', '8-30d', '31-90d', '>90d'])
  })
})
