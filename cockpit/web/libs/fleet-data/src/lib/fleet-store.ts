import { DOCUMENT } from '@angular/common'
import { Injectable, InjectionToken, computed, inject, signal } from '@angular/core'
import { FleetClient, FleetRequestError, FleetSchemaError, SchemaMismatch } from './fleet-client'
import { FleetModel, FleetModels, ModelOptions } from './fleet-model'
import { canReadContent, emptyDocument, machineOptions, ownerRoutes, repositoryLabel } from './fleet-view'
import { FleetDocument, SCHEMA_VERSION, Session, SessionStatus } from './fleet.types'

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

/** The schema version this page reads; a test sets another to see each mismatch message. */
export const EXPECTED_SCHEMA = new InjectionToken<number>('expected schema version', {
  providedIn: 'root',
  factory: () => SCHEMA_VERSION,
})

/** The clock, derivation counter and SSH routes of the view model. */
export const MODEL_OPTIONS = new InjectionToken<ModelOptions>('fleet model options', {
  providedIn: 'root',
  factory: () => ({}),
})

/**
 * The one fleet store: it holds the last document and the session, re-reads
 * the document with If-None-Match, and never waits for a full scan. While the
 * daemon is warming up it shows what has been scanned so far. It reads only while
 * the page is visible: a hidden tab pauses it, and the page coming back reads at
 * once, and reads the session again, so an expired or a new owner session is noticed
 * (as it is when a read is answered 401).
 */
@Injectable({ providedIn: 'root' })
export class FleetStore {
  private readonly client = inject(FleetClient)
  private readonly intervals = inject(POLL_INTERVALS)
  private readonly expected = inject(EXPECTED_SCHEMA)
  private readonly models = new FleetModels(inject(MODEL_OPTIONS))
  private readonly doc = inject(DOCUMENT)
  private etag: string | undefined
  private digest: string | undefined
  private timer: ReturnType<typeof setTimeout> | undefined
  private running = false
  private failures = 0
  private loadingSession = false
  private polling = false
  private readonly onVisibility = (): void => this.visibilityChanged()

  readonly document = signal<FleetDocument>(emptyDocument())
  /** Set while the daemon speaks another schema version: no data is shown, and what to do about it. */
  /** How many entries of the last document were dropped for lacking a required field: for a diagnostic line. */
  readonly droppedEntries = signal(0)
  readonly schemaMismatch = signal<SchemaMismatch | null>(null)
  /** The view model of the current document: derived collections are computed once per document. */
  readonly model = computed<FleetModel>(() => this.models.forDocument(this.document(), this.now(), ownerRoutes(this.session())))
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
    this.doc.addEventListener('visibilitychange', this.onVisibility)
    void this.poll()
  }

  stop(): void {
    this.running = false
    clearTimeout(this.timer)
    this.doc.removeEventListener('visibilitychange', this.onVisibility)
  }

  /** A hidden page schedules nothing more; one that is visible again reads at once (unless a read is already out) and re-reads the session. */
  private visibilityChanged(): void {
    if (this.doc.visibilityState === 'hidden') {
      clearTimeout(this.timer)
      return
    }
    void this.loadSession(true)
    if (this.polling) return
    clearTimeout(this.timer)
    void this.poll()
  }

  /** One read of the document, then the next one is scheduled. */
  async poll(): Promise<void> {
    // The session read is retried on every poll until it succeeds.
    if (this.session() === null) void this.loadSession()
    this.polling = true
    try {
      const read = await this.client.readFleet(this.etag, this.expected)
      if (read.kind === 'changed') {
        this.etag = read.etag
        // A body identical to the last one keeps the document, so nothing downstream recomputes.
        if (read.digest !== this.digest) {
          this.document.set(read.document)
          this.droppedEntries.set(read.dropped ?? 0)
        }
        this.digest = read.digest
      }
      this.schemaMismatch.set(null)
      this.error.set(null)
      this.failures = 0
    } catch (error) {
      if (error instanceof FleetSchemaError) {
        // Another schema version renders no data at all.
        this.etag = undefined
        this.digest = undefined
        this.schemaMismatch.set(error.mismatch)
        this.document.set({ ...emptyDocument(), warming_up: false })
        this.droppedEntries.set(0)
      }
      this.error.set(error instanceof Error ? error.message : String(error))
      this.failures++
      // An answer of 401 says the session this page holds is no longer the one the daemon sees.
      if (error instanceof FleetRequestError && error.status === 401) void this.loadSession(true)
    }
    this.polling = false
    this.loaded.set(true)
    this.now.set(Date.now())
    // A hidden page reads nothing more until it is visible again (see `visibilityChanged`).
    if (!this.running || this.doc.visibilityState === 'hidden') return
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

  /** Reads the session; `refresh` reads it again though there is one, and a failed re-read keeps the session it had. */
  private async loadSession(refresh = false): Promise<void> {
    if (this.loadingSession) return
    this.loadingSession = true
    try {
      this.session.set(await this.client.readSession())
      this.sessionStatus.set('ready')
    } catch {
      // Without a session the page still lists the fleet; it has no code links.
      if (!refresh || this.session() === null) {
        this.session.set(null)
        this.sessionStatus.set('failed')
      }
    } finally {
      this.loadingSession = false
    }
  }
}
