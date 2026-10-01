import { Injectable, InjectionToken, computed, inject, signal } from '@angular/core'
import { FleetClient } from './fleet-client'
import { canReadContent, emptyDocument, machineOptions, repositoryLabel } from './fleet-view'
import { FleetDocument, Session, SessionStatus } from './fleet.types'

/** How often the store re-reads the fleet document, in milliseconds. */
export interface PollIntervals {
  /** While the daemon's first pass is still running. */
  warmingUp: number
  /** Once the document is complete. */
  steady: number
}

export const POLL_INTERVALS = new InjectionToken<PollIntervals>('poll intervals', {
  providedIn: 'root',
  factory: () => ({ warmingUp: 2_000, steady: 15_000 }),
})

/**
 * The one fleet store: it holds the last document and the session, re-reads
 * the document with If-None-Match, and never waits for a full scan. While the
 * daemon is warming up it shows what has been scanned so far.
 */
@Injectable({ providedIn: 'root' })
export class FleetStore {
  private readonly client = inject(FleetClient)
  private readonly intervals = inject(POLL_INTERVALS)
  private etag: string | undefined
  private timer: ReturnType<typeof setTimeout> | undefined
  private running = false
  private failures = 0
  private loadingSession = false

  readonly document = signal<FleetDocument>(emptyDocument())
  readonly session = signal<Session | null>(null)
  /** Whether the session read is still going ('loading'), answered ('ready') or failed and will be retried ('failed'). */
  readonly sessionStatus = signal<SessionStatus>('loading')
  /** Set until the first read has been answered. */
  readonly loaded = signal(false)
  readonly error = signal<string | null>(null)
  /** The clock ages are measured against; it moves with every read. */
  readonly now = signal(Date.now())

  readonly warmingUp = computed(() => this.document().warming_up)
  readonly progress = computed(() => {
    const { repositories_scanned: scanned, repositories_total: total } = this.document()
    return { scanned, total }
  })
  readonly codeBrowserUrl = computed(() => this.session()?.code_browser_url)
  /** The configured code-index provider's name, or undefined when none is configured. */
  readonly codeIndexProvider = computed(() => this.document().code_index_provider)
  /** Whether this caller holds an owner session, which is what reading a README takes. */
  readonly canReadContent = computed(() => canReadContent(this.session()))
  readonly machineOptions = computed(() => machineOptions(this.document().machines))
  readonly repositoryById = computed(() => new Map(this.document().repositories.map((repository) => [repository.id, repository])))

  /** A repository's name for a row, or its id when the document does not list it, or a dash for none. */
  repositoryName(id: string | undefined): string {
    const repository = id === undefined ? undefined : this.repositoryById().get(id)
    return repository ? repositoryLabel(repository) : (id ?? '—')
  }

  /** Starts reading; calling it again while running does nothing. */
  start(): void {
    if (this.running) return
    this.running = true
    void this.poll()
  }

  stop(): void {
    this.running = false
    clearTimeout(this.timer)
  }

  /** One read of the document, then the next one is scheduled. */
  async poll(): Promise<void> {
    // The session read is retried on every poll until it succeeds.
    if (this.session() === null) void this.loadSession()
    try {
      const read = await this.client.readFleet(this.etag)
      if (read.kind === 'changed') {
        this.etag = read.etag
        this.document.set(read.document)
      }
      this.error.set(null)
      this.failures = 0
    } catch (error) {
      this.error.set(error instanceof Error ? error.message : String(error))
      this.failures++
    }
    this.loaded.set(true)
    this.now.set(Date.now())
    if (!this.running) return
    // After a failure, back off: double the fast interval each time, up to the slow one.
    const delay =
      this.failures > 0
        ? Math.min(this.intervals.steady, this.intervals.warmingUp * 2 ** this.failures)
        : this.warmingUp()
          ? this.intervals.warmingUp
          : this.intervals.steady
    // A restart during an in-flight read must not leave two loops running.
    clearTimeout(this.timer)
    this.timer = setTimeout(() => void this.poll(), delay)
  }

  private async loadSession(): Promise<void> {
    if (this.loadingSession) return
    this.loadingSession = true
    try {
      this.session.set(await this.client.readSession())
      this.sessionStatus.set('ready')
    } catch {
      // Without a session the page still lists the fleet; it has no code links.
      this.session.set(null)
      this.sessionStatus.set('failed')
    } finally {
      this.loadingSession = false
    }
  }
}
