import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { CLOCK_TICK_MS, FreshnessChip, ageText } from './freshness-chip'

const NOW = Date.parse('2026-10-01T10:05:00Z')

describe('ageText', () => {
  it('uses the largest whole unit', () => {
    expect([0, 70, 119, 120, 3599, 7200, 90_000, 172_800, 400_000].map(ageText)).toEqual(['0 s', '70 s', '119 s', '2 min', '59 min', '2 h', '25 h', '2 d', '4 d'])
  })
})

describe('FreshnessChip', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
  })
  afterEach(() => vi.useRealTimers())

  function render(prepare: (store: FleetStore) => void) {
    const store = TestBed.inject(FleetStore)
    prepare(store)
    const fixture = TestBed.createComponent(FreshnessChip)
    const chip = () => fixture.nativeElement.querySelector('.chip') as HTMLElement
    return { fixture, chip, store }
  }

  const loaded = (store: FleetStore, extra = {}) => {
    store.loaded.set(true)
    store.document.set(fleetDocument(extra))
  }

  it('says how old the snapshot is, and turns amber after two refresh intervals', async () => {
    // The fixture's snapshot is five minutes old and its interval 30 seconds.
    const { fixture, chip } = render((store) => loaded(store, { snapshot_at: new Date(NOW - 70_000).toISOString() }))
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('updated 70 s ago')
    expect(chip().classList.contains('tone-warn')).toBe(true)
    expect(chip().getAttribute('title')).toContain('two refresh intervals of 30 s')
  })

  it('is calm while the snapshot is within two intervals, and ticks with the clock', async () => {
    const { fixture, chip } = render((store) => loaded(store, { snapshot_at: new Date(NOW - 50_000).toISOString() }))
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('updated 50 s ago')
    expect(chip().classList.contains('tone-ok')).toBe(true)
    await vi.advanceTimersByTimeAsync(11 * CLOCK_TICK_MS)
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('updated 61 s ago')
    expect(chip().classList.contains('tone-warn')).toBe(true)
    fixture.destroy()
  })

  it('assumes an interval for a document that carries none, and never reads a negative age', async () => {
    const { fixture, chip } = render((store) => loaded(store, { refresh_interval_seconds: undefined, snapshot_at: new Date(NOW + 5000).toISOString() }))
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('updated 0 s ago')
    expect(chip().classList.contains('tone-ok')).toBe(true)
  })

  it('shows the scanned count while the daemon warms up', async () => {
    const { fixture, chip } = render((store) => loaded(store, { warming_up: true, repositories_scanned: 120, repositories_total: 438 }))
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('scanned 120 of 438')
  })

  it('says it is connecting before the first read, and when the snapshot time is unknown', async () => {
    const { fixture, chip, store } = render(() => undefined)
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('connecting')
    loaded(store, { snapshot_at: undefined })
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('snapshot time unknown')
  })

  it('says the data is not readable on a schema mismatch', async () => {
    const { fixture, chip } = render((store) => {
      loaded(store)
      store.schemaMismatch.set('page-older')
    })
    await fixture.whenStable()
    expect(chip().textContent?.trim()).toBe('not readable')
    expect(chip().classList.contains('tone-bad')).toBe(true)
  })
})
