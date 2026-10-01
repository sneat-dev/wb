import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { Shortcuts } from '../shortcuts/shortcuts'
import { TestBed } from '@angular/core/testing'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FETCH, FleetStore, MACHINE_METRICS_PATH } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { appRoutes } from '../app.routes'
import { METRICS_INTERVAL_MS } from './metrics-poller'

// cockpit-views#ac:metrics-poll-only-while-visible: Home, then Repositories, then Machines, 30 seconds each.
describe('machine metrics polling on the pages', () => {
  const requests: string[] = []

  beforeEach(() => {
    requests.length = 0
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      requests.push(String(input))
      return new Response(JSON.stringify({ machine: 'x', route: 'local', samples: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    TestBed.configureTestingModule({ providers: [provideRouter(appRoutes, withComponentInputBinding()), { provide: FETCH, useValue: fetcher }, { provide: LIST_SHORTCUTS, useExisting: Shortcuts }] })
    const store = TestBed.inject(FleetStore)
    store.loaded.set(true)
    store.document.set(fleetDocument())
  })

  afterEach(() => vi.useRealTimers())

  it('requests every 10 seconds while Home and Machines are shown, and not at all on Repositories', async () => {
    const harness = await RouterTestingHarness.create()
    const metricsRequests = () => requests.filter((url) => url.startsWith(MACHINE_METRICS_PATH))

    // The machine strip, whose polling it is, is part of Home's lazy chunk: have the module loaded so the page gets it at once.
    await import('../pages/home/home-rest')
    await harness.navigateByUrl('/')
    await vi.advanceTimersByTimeAsync(30_000 - 1)
    // Two machines, read at 0, 10 and 20 seconds.
    expect(metricsRequests()).toHaveLength(3 * 2)
    expect(metricsRequests()[0]).toBe(`${MACHINE_METRICS_PATH}?machine=mach-alpha`)

    await harness.navigateByUrl('/repositories')
    await vi.advanceTimersByTimeAsync(30_000)
    expect(metricsRequests()).toHaveLength(3 * 2)

    await harness.navigateByUrl('/machines')
    await vi.advanceTimersByTimeAsync(30_000 - 1)
    expect(metricsRequests()).toHaveLength(6 * 2)

    // Leaving Machines for a detail page of another list stops it again.
    await harness.navigateByUrl('/worktrees')
    await vi.advanceTimersByTimeAsync(3 * METRICS_INTERVAL_MS)
    expect(metricsRequests()).toHaveLength(6 * 2)
  })

  it('polls only the machines the Machines page shows', async () => {
    const harness = await RouterTestingHarness.create()
    await harness.navigateByUrl('/machines?machine=mach-beta')
    await vi.advanceTimersByTimeAsync(0)
    expect(requests.filter((url) => url.startsWith(MACHINE_METRICS_PATH))).toEqual([`${MACHINE_METRICS_PATH}?machine=mach-beta`])
  })

  // cockpit-views#ac:metrics-poll-only-while-visible: the machine's own page reads its machine, every 10 seconds, only while it is shown.
  it('polls only the machine of a machine page, and stops when the page is left', async () => {
    const harness = await RouterTestingHarness.create()
    await harness.navigateByUrl('/machines/mach-beta')
    await vi.advanceTimersByTimeAsync(30_000 - 1)
    const metricsRequests = () => requests.filter((url) => url.startsWith(MACHINE_METRICS_PATH))
    expect(metricsRequests()).toEqual(Array(3).fill(`${MACHINE_METRICS_PATH}?machine=mach-beta`))
    await harness.navigateByUrl('/worktrees')
    await vi.advanceTimersByTimeAsync(3 * METRICS_INTERVAL_MS)
    expect(metricsRequests()).toHaveLength(3)
  })
})
