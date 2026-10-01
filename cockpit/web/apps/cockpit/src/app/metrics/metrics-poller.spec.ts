import { Component } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { FleetClient, MachineMetrics } from '@cockpit/fleet-data'
import { METRICS_INTERVAL_MS, MetricsPoller, watchMetrics } from './metrics-poller'

function metrics(machine: string): MachineMetrics {
  return { machine, route: 'local', samples: [] }
}

describe('MetricsPoller', () => {
  const read = vi.fn<(machine: string) => Promise<MachineMetrics>>()

  beforeEach(() => {
    vi.useFakeTimers()
    read.mockReset().mockImplementation(async (machine) => metrics(machine))
    TestBed.configureTestingModule({ providers: [{ provide: FleetClient, useValue: { readMachineMetrics: read } }] })
  })

  afterEach(() => {
    vi.useRealTimers()
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
  })

  const advance = (ms: number) => vi.advanceTimersByTimeAsync(ms)

  it('reads at once, then every 10 seconds, for the machines asked for, and stops with the last page', async () => {
    const poller = TestBed.inject(MetricsPoller)
    let ids = ['m1', 'm2']
    const stop = poller.watch(() => ids)
    await advance(0)
    expect(read.mock.calls.map((call) => call[0])).toEqual(['m1', 'm2'])
    expect(poller.metricsOf('m1')?.machine).toBe('m1')
    expect(poller.metricsOf('nobody')).toBeUndefined()

    ids = ['m2']
    await advance(METRICS_INTERVAL_MS - 1)
    expect(read).toHaveBeenCalledTimes(2)
    await advance(1)
    expect(read).toHaveBeenCalledTimes(3)
    await advance(METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(4)

    stop()
    await advance(5 * METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(4)
  })

  it('asks once per machine however many pages ask for it, and polls until the last page leaves', async () => {
    const poller = TestBed.inject(MetricsPoller)
    const first = poller.watch(() => ['m1'])
    const second = poller.watch(() => ['m1', 'm2'])
    await advance(0)
    expect(read.mock.calls.map((call) => call[0])).toEqual(['m1', 'm2'])
    first()
    await advance(METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(4)
    second()
    await advance(METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(4)
  })

  it('keeps the last good answer when a read fails and says why, and recovers', async () => {
    const poller = TestBed.inject(MetricsPoller)
    poller.watch(() => ['m1'])
    await advance(0)
    read.mockRejectedValueOnce(new Error('HTTP 502'))
    await advance(METRICS_INTERVAL_MS)
    expect(poller.entries().get('m1')).toMatchObject({ error: 'HTTP 502', metrics: { machine: 'm1' } })
    read.mockRejectedValueOnce('odd')
    await advance(METRICS_INTERVAL_MS)
    expect(poller.entries().get('m1')?.error).toBe('odd')
    await advance(METRICS_INTERVAL_MS)
    expect(poller.entries().get('m1')?.error).toBeUndefined()
  })

  it('does not poll while the browser tab is hidden, and resumes when it shows again', async () => {
    const poller = TestBed.inject(MetricsPoller)
    poller.watch(() => ['m1'])
    await advance(0)
    expect(read).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await advance(3 * METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await advance(0)
    expect(read).toHaveBeenCalledTimes(2)
    // A second visibility event while polling changes nothing.
    document.dispatchEvent(new Event('visibilitychange'))
    await advance(0)
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('does not start a second loop when the pages leave and return while a read is out', async () => {
    const poller = TestBed.inject(MetricsPoller)
    let finish: (value: MachineMetrics) => void = () => undefined
    read.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)))
    const stop = poller.watch(() => ['m1'])
    await advance(0)
    stop()
    poller.watch(() => ['m1'])
    await advance(0)
    expect(read).toHaveBeenCalledTimes(2)
    finish(metrics('m1'))
    await advance(0)
    await advance(METRICS_INTERVAL_MS)
    // One loop: one read per interval.
    expect(read).toHaveBeenCalledTimes(3)
  })

  it('stops with the application, and watchMetrics ends with its component', async () => {
    @Component({ template: '' })
    class Page {
      constructor() {
        watchMetrics(() => ['m1'])
      }
    }
    const fixture = TestBed.createComponent(Page)
    await advance(0)
    expect(read).toHaveBeenCalledTimes(1)
    fixture.destroy()
    await advance(2 * METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(1)

    TestBed.inject(MetricsPoller).watch(() => ['m1'])
    await advance(0)
    TestBed.resetTestingModule()
    await advance(2 * METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(2)
  })
})
