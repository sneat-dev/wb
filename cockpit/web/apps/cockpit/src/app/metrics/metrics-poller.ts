import { DOCUMENT } from '@angular/common'
import { DestroyRef, Injectable, InjectionToken, inject, signal } from '@angular/core'
import { FleetClient, FleetRequestError, MachineMetrics } from '@cockpit/fleet-data'

/** How often the machine metrics are read while a page that shows them is visible (REQ:machine-metrics-polling). */
export const METRICS_INTERVAL_MS = 10_000

/** The longest wait between rounds after the daemon keeps refusing or failing, in milliseconds. */
export const METRICS_MAX_BACKOFF_MS = 120_000

export const METRICS_INTERVAL = new InjectionToken<number>('metrics poll interval in milliseconds', {
  providedIn: 'root',
  factory: () => METRICS_INTERVAL_MS,
})

/** The last answer for one machine: its metrics, and the failure of the last read when it failed. */
export interface MetricsEntry {
  /** The last answer that came; kept while a later read fails. */
  metrics?: MachineMetrics
  /** Why the last read failed; undefined after a good read. */
  error?: string
  /** When the last read finished, in milliseconds. */
  readAt: number
}

/**
 * Reads `GET /api/v1/cockpit/machine-metrics?machine=<id>` every 10 seconds for
 * the machines the shown pages ask for, and only while a page is shown and the
 * browser tab is visible. A page that shows metrics calls `watch` (or
 * `watchMetrics` from its constructor) and the polling starts with the first
 * such page and stops with the last; there is one request per machine per
 * round, however many pages ask for it, and the next round starts when the
 * last one has finished.
 */
@Injectable({ providedIn: 'root' })
export class MetricsPoller {
  private readonly client = inject(FleetClient)
  private readonly interval = inject(METRICS_INTERVAL)
  private readonly doc = inject(DOCUMENT)
  private readonly watchers = new Set<() => readonly string[]>()
  private timer: ReturnType<typeof setTimeout> | undefined
  private polling = false
  private controller: AbortController | undefined
  /** The reads of the latest round: the next one starts after them. */
  private settled: Promise<void> = Promise.resolve()
  private failures = 0
  /** Counts the starts, so a round that began before a stop does not schedule a second loop after a restart. */
  private generation = 0

  /** The latest answer per machine id. */
  readonly entries = signal<ReadonlyMap<string, MetricsEntry>>(new Map())

  constructor() {
    const reconcile = () => this.reconcile()
    this.doc.addEventListener('visibilitychange', reconcile)
    inject(DestroyRef).onDestroy(() => {
      this.doc.removeEventListener('visibilitychange', reconcile)
      this.stop()
    })
  }

  /** A page that shows the metrics of the machines `ids()` returns; the returned function ends it. */
  watch(ids: () => readonly string[]): () => void {
    this.watchers.add(ids)
    this.reconcile()
    return () => {
      this.watchers.delete(ids)
      this.reconcile()
    }
  }

  /** The metrics of one machine, when they have been read. */
  metricsOf(machineId: string): MachineMetrics | undefined {
    return this.entries().get(machineId)?.metrics
  }

  private reconcile(): void {
    const active = this.watchers.size > 0 && this.doc.visibilityState !== 'hidden'
    if (active && !this.polling) {
      this.polling = true
      const generation = ++this.generation
      // A page calls `watch` from its constructor, before the router has bound its inputs
      // (the machine filter of the address): the first read comes just after that.
      this.timer = setTimeout(() => void this.round(generation), 0)
    } else if (!active) {
      this.stop()
    }
  }

  private stop(): void {
    this.polling = false
    this.generation++
    this.controller?.abort()
    this.controller = undefined
    this.failures = 0
    clearTimeout(this.timer)
  }

  /**
   * One read of every machine asked for, then the next one is scheduled, whatever happens in
   * between (a page whose machine list throws must not end the polling). A round starts only after
   * the reads of the one before it have settled, so a page change never overlaps two rounds, and
   * what comes back for a generation that has been stopped is discarded.
   */
  private async round(generation: number): Promise<void> {
    await this.settled
    if (generation !== this.generation) return
    const controller = new AbortController()
    this.controller = controller
    const reads = this.readAll(controller.signal)
    this.settled = reads.then(
      () => undefined,
      () => undefined,
    )
    try {
      const results = await reads
      if (!controller.signal.aborted) this.record(results)
    } catch (error) {
      console.error('reading the machine metrics failed', error)
      this.failures++
    } finally {
      // A page that left, or a hidden tab, while the reads were out ends the loop.
      if (generation === this.generation) this.timer = setTimeout(() => void this.round(generation), this.delay())
    }
  }

  private async readAll(signal: AbortSignal): Promise<[string, MachineMetrics | Error][]> {
    const ids = new Set([...this.watchers].flatMap((ids) => ids()))
    return Promise.all(
      [...ids].map(async (id): Promise<[string, MachineMetrics | Error]> => {
        try {
          // The client cannot be cancelled, so a read of a stopped generation is let finish and dropped.
          const metrics = await this.client.readMachineMetrics(id)
          return signal.aborted ? [id, new Error('stopped')] : [id, metrics]
        } catch (error) {
          return [id, error instanceof Error ? error : new Error(String(error))]
        }
      }),
    )
  }

  private record(results: [string, MachineMetrics | Error][]): void {
    const next = new Map(this.entries())
    const readAt = Date.now()
    let refused = false
    for (const [id, result] of results) {
      if (result instanceof Error) {
        next.set(id, { metrics: next.get(id)?.metrics, error: result.message, readAt })
        // The daemon refusing the request or failing: asking more often will not help.
        if (result instanceof FleetRequestError && (result.status === 401 || result.status >= 500)) refused = true
      } else {
        next.set(id, { metrics: result, readAt })
      }
    }
    this.failures = refused ? this.failures + 1 : 0
    this.entries.set(next)
  }

  /** The wait before the next round: the interval, doubled for each round in a row that the daemon refused or failed, up to two minutes. */
  private delay(): number {
    return Math.min(this.interval * 2 ** this.failures, METRICS_MAX_BACKOFF_MS)
  }
}

/** Watches the machines `ids()` returns for as long as the calling component lives. Call it from a constructor. */
export function watchMetrics(ids: () => readonly string[]): void {
  const stop = inject(MetricsPoller).watch(ids)
  inject(DestroyRef).onDestroy(stop)
}
