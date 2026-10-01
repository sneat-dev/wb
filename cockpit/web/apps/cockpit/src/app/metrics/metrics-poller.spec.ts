import { Component } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { FleetClient, FleetRequestError, MachineMetrics } from '@cockpit/fleet-data'
import { METRICS_INTERVAL_MS, METRICS_MAX_BACKOFF_MS, MetricsPoller, watchMetrics } from './metrics-poller'

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

  it('does not overlap rounds, nor start a second loop, when the pages change while a read is out', async () => {
    const poller = TestBed.inject(MetricsPoller)
    let finish: (value: MachineMetrics) => void = () => undefined
    let inFlight = 0
    let overlapped = false
    read.mockImplementation(
      (machine) =>
        new Promise((resolve) => {
          if (++inFlight > 1) overlapped = true
          finish = (value) => {
            inFlight--
            resolve(value)
          }
          void machine
        }),
    )
    // Home is shown, then Machines replaces it while Home's read is still out.
    const home = poller.watch(() => ['m1'])
    await advance(0)
    home()
    poller.watch(() => ['m1'])
    await advance(0)
    expect(read).toHaveBeenCalledTimes(1)
    finish(metrics('m1'))
    await advance(0)
    // The old generation's answer is dropped; the new round reads once the old one has settled.
    expect(read).toHaveBeenCalledTimes(2)
    finish(metrics('m1'))
    await advance(METRICS_INTERVAL_MS)
    finish(metrics('m1'))
    await advance(0)
    expect(overlapped).toBe(false)
    expect(read).toHaveBeenCalledTimes(3)
  })

  it('lets a round that was waiting for the one before it give up when its pages have gone', async () => {
    const poller = TestBed.inject(MetricsPoller)
    let finish: (value: MachineMetrics) => void = () => undefined
    read.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)))
    const first = poller.watch(() => ['m1'])
    await advance(0)
    first()
    const second = poller.watch(() => ['m1'])
    await advance(0)
    second()
    finish(metrics('m1'))
    await advance(5 * METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('drops what a stopped generation read, and keeps the loop alive when a page throws asking for its machines', async () => {
    const poller = TestBed.inject(MetricsPoller)
    let finish: (value: MachineMetrics) => void = () => undefined
    read.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)))
    const stop = poller.watch(() => ['m1'])
    await advance(0)
    stop()
    finish(metrics('m1'))
    await advance(0)
    expect(poller.entries().size).toBe(0)

    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    let throws = true
    poller.watch(() => {
      if (throws) throw new Error('page broke')
      return ['m1']
    })
    await advance(0)
    expect(error).toHaveBeenCalled()
    throws = false
    await advance(2 * METRICS_INTERVAL_MS)
    expect(poller.metricsOf('m1')?.machine).toBe('m1')
    error.mockRestore()
  })

  it('backs off exponentially, up to two minutes, while the daemon refuses or fails, and recovers', async () => {
    const poller = TestBed.inject(MetricsPoller)
    read.mockRejectedValue(new FleetRequestError(502))
    poller.watch(() => ['m1'])
    await advance(0)
    expect(read).toHaveBeenCalledTimes(1)
    // One failure: the next read after 20 s, then 40 s, 80 s, then 120 s at most.
    for (const [wait, total] of [[METRICS_INTERVAL_MS * 2, 2], [METRICS_INTERVAL_MS * 4, 3], [METRICS_INTERVAL_MS * 8, 4], [METRICS_MAX_BACKOFF_MS, 5], [METRICS_MAX_BACKOFF_MS, 6]]) {
      await advance(wait - 1)
      expect(read).toHaveBeenCalledTimes(total - 1)
      await advance(1)
      expect(read).toHaveBeenCalledTimes(total)
    }
    read.mockResolvedValue(metrics('m1'))
    await advance(METRICS_MAX_BACKOFF_MS)
    await advance(METRICS_INTERVAL_MS)
    expect(read.mock.calls.length).toBeGreaterThanOrEqual(8)
  })

  it('treats a refusal of 401 as a reason to back off, and a 404 as not', async () => {
    const poller = TestBed.inject(MetricsPoller)
    read.mockRejectedValue(new FleetRequestError(404))
    poller.watch(() => ['m1'])
    await advance(0)
    await advance(METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(2)
    read.mockRejectedValue(new FleetRequestError(401))
    await advance(METRICS_INTERVAL_MS)
    await advance(METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(3)
    await advance(METRICS_INTERVAL_MS)
    expect(read).toHaveBeenCalledTimes(4)
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
