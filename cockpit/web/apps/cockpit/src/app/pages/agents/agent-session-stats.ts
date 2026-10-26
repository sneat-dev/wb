import { ChangeDetectionStrategy, Component, computed, effect, inject, signal } from '@angular/core'
import { FleetStore, FETCH } from '@cockpit/fleet-data'
import { AgentSessionStatsBucket, AgentSessionStatsReport, readAgentSessionStats } from '@cockpit/fleet-data/lazy-client'

/** Local, owner-only history of WB-dispatched work grouped by caller and repository. */
@Component({
  selector: 'app-agent-session-stats',
  templateUrl: './agent-session-stats.html',
  styleUrl: './agent-session-stats.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgentSessionStats {
  protected readonly store = inject(FleetStore)
  private readonly fetcher = inject(FETCH)
  protected readonly report = signal<AgentSessionStatsReport | null>(null)
  protected readonly loading = signal(false)
  protected readonly error = signal(false)
  protected readonly query = signal('')
  protected readonly days = signal<7 | 30 | 90>(30)
  protected readonly owner = computed(() => this.store.session()?.principal === 'owner')
  private requestVersion = 0

  protected readonly rows = computed(() => {
    const query = this.query().trim().toLowerCase()
    return (this.report()?.by_session_repository ?? [])
      .filter((row) => !query || `${row.session_name ?? ''} ${row.session_id} ${row.repository}`.toLowerCase().includes(query))
      .slice()
      .sort((a, b) => b.runs - a.runs || a.session_id.localeCompare(b.session_id) || a.repository.localeCompare(b.repository))
  })

  constructor() {
    effect(() => {
      if (this.owner()) {
        void this.load()
      } else {
        this.requestVersion++
        this.report.set(null)
        this.loading.set(false)
        this.error.set(false)
      }
    })
  }

  protected search(event: Event): void {
    this.query.set((event.target as HTMLInputElement).value)
  }

  protected setWindow(event: Event): void {
    const selected = Number((event.target as HTMLSelectElement).value)
    if (selected !== 7 && selected !== 30 && selected !== 90) return
    this.days.set(selected)
    this.report.set(null)
    void this.load()
  }

  protected refresh(): void {
    void this.load()
  }

  private async load(): Promise<void> {
    const version = ++this.requestVersion
    this.loading.set(true)
    this.error.set(false)
    try {
      const report = await readAgentSessionStats(this.fetcher, this.days())
      if (version === this.requestVersion && this.owner()) this.report.set(report)
    } catch {
      if (version === this.requestVersion && this.owner()) {
        this.report.set(null)
        this.error.set(true)
      }
    } finally {
      if (version === this.requestVersion) this.loading.set(false)
    }
  }

  protected readonly count = (value: number): string => new Intl.NumberFormat('en-US').format(value)
  protected readonly duration = (milliseconds: number): string => {
    const seconds = milliseconds / 1000
    if (seconds < 60) return `${seconds.toFixed(1)} s`
    if (seconds < 3600) return `${(seconds / 60).toFixed(1)} min`
    return `${(seconds / 3600).toFixed(1)} h`
  }
  protected readonly tokens = (row: AgentSessionStatsBucket): string =>
    row.usage_runs === 0 ? '—' : `${this.count(row.input_tokens)} in · ${this.count(row.output_tokens)} out`
  protected readonly cost = (row: AgentSessionStatsBucket): string =>
    row.cost_runs === 0 ? '—' : `$${row.estimated_usd.toFixed(4)}`
}
