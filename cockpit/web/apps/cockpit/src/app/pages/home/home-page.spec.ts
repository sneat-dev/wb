import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FETCH, FleetStore } from '@cockpit/fleet-data'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { ClipboardWriter, UiClock } from '@cockpit/ui/control'
import { appRoutes } from '../../app.routes'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { NOW, fleet } from './home-testing'
import { HomePage } from './home-page'

const headings = (root: HTMLElement) => [...root.querySelectorAll('h2')].map((heading) => (heading.textContent ?? '').replace(/\s+/g, ' ').trim())

// Home opened through the application's routes, with a document in the store and a stubbed fetch (the test harness of the
// pages, with the clock and the chart engine replaced).
async function open(document = fleet()) {
  const requests: string[] = []
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input))
    return new Response('{}', { status: 404 })
  })
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [
      provideRouter(appRoutes, withComponentInputBinding()),
      { provide: FETCH, useValue: fetcher },
      { provide: UiClock, useValue: { now: signal(NOW) } },
      { provide: CHART_ENGINE, useValue: async () => ({ create: () => ({ update: vi.fn(), destroy: vi.fn() }) }) },
      { provide: ClipboardWriter, useValue: { copy: vi.fn() } },
    ],
  })
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.now.set(NOW)
  store.loaded.set(true)
  const harness = await RouterTestingHarness.create()
  await harness.navigateByUrl('/', HomePage)
  await harness.fixture.whenStable()
  return { harness, store, requests, root: harness.routeNativeElement as HTMLElement }
}

describe('HomePage', () => {
  it('renders on its own, over an empty fleet that is still warming up', async () => {
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: FETCH, useValue: async () => new Response('{}', { status: 404 }) }] })
    const fixture = TestBed.createComponent(HomePage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h2')?.textContent).toContain('Needs you')
    expect(fixture.nativeElement.querySelector('app-lazy-mount')).not.toBeNull()
  })

  it('renders "Needs you" at once from the model that is already loaded', async () => {
    const { root } = await open()
    expect(headings(root)[0]).toBe('Needs you 6')
    expect(root.querySelectorAll('.needs-row')).toHaveLength(6)
  })

  it('then appends the rest of Home from its lazy chunk, below, in order', async () => {
    const { harness, root } = await open()
    await vi.waitFor(async () => {
      await harness.fixture.whenStable()
      expect(headings(root)).toEqual(['Needs you 6', 'Ready to land 2', 'In flight 4', 'Resume', 'Cleanup', 'Fleet health 2', 'Throughput'])
    })
    expect(root.querySelector('app-home-rest')).not.toBeNull()
  })

  it('keeps the rest current with the model, and shows a skeleton and not "Nothing needs you" while the daemon is scanning', async () => {
    const { harness, root, store } = await open({ ...fleet('warming'), warming_up: true })
    expect(root.querySelector('.home-calm')).toBeNull()
    expect(root.querySelector('app-skeleton-rows')).not.toBeNull()
    await vi.waitFor(async () => {
      await harness.fixture.whenStable()
      expect(root.querySelector('app-home-rest')).not.toBeNull()
    })
    store.document.set({ ...fleet('healthy'), warming_up: false })
    await vi.waitFor(async () => {
      await harness.fixture.whenStable()
      expect(root.querySelector('.home-calm')?.textContent).toContain('Nothing needs you.')
    })
  })

  it('polls the metrics of the machines while it is shown (REQ:machine-metrics-polling)', async () => {
    const { requests } = await open()
    await vi.waitFor(() => expect(requests.filter((url) => url.startsWith('/api/v1/cockpit/machine-metrics'))).toHaveLength(3))
    expect(TestBed.inject(MetricsPoller)).toBeDefined()
  })
})
