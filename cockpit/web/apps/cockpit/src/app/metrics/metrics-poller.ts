import { DOCUMENT } from '@angular/common'
import { DestroyRef, Injectable, InjectionToken, inject, signal } from '@angular/core'
import { FleetClient, MachineMetrics } from '@cockpit/fleet-data'

/** How often the machine metrics are read while a page that shows them is visible (REQ:machine-metrics-polling). */
export const METRICS_INTERVAL_MS = 10_000

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
    clearTimeout(this.timer)
  }

  /** One read of every machine asked for, then the next one is scheduled. */
  private async round(generation: number): Promise<void> {
    const ids = new Set([...this.watchers].flatMap((ids) => ids()))
    const results = await Promise.all(
      [...ids].map(async (id): Promise<[string, MachineMetrics | Error]> => {
        try {
          return [id, await this.client.readMachineMetrics(id)]
        } catch (error) {
          return [id, error instanceof Error ? error : new Error(String(error))]
        }
      }),
    )
    const next = new Map(this.entries())
    const readAt = Date.now()
    for (const [id, result] of results) {
      next.set(id, result instanceof Error ? { metrics: next.get(id)?.metrics, error: result.message, readAt } : { metrics: result, readAt })
    }
    this.entries.set(next)
    // A page that left, or a hidden tab, while the reads were out ends the loop.
    if (generation === this.generation) {
      this.timer = setTimeout(() => void this.round(generation), this.interval)
    }
  }
}

/** Watches the machines `ids()` returns for as long as the calling component lives. Call it from a constructor. */
export function watchMetrics(ids: () => readonly string[]): void {
  const stop = inject(MetricsPoller).watch(ids)
  inject(DestroyRef).onDestroy(stop)
}
