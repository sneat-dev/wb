import { ChangeDetectionStrategy, Component, DestroyRef, computed, inject, signal } from '@angular/core'
import { FleetStore } from '@cockpit/fleet-data'
import { Icon, IconName } from '../ui/icon'

/** The refresh interval assumed for a document that does not carry one. */
export const DEFAULT_REFRESH_SECONDS = 30

/** How often the chip's clock moves, in milliseconds. */
export const CLOCK_TICK_MS = 1000

/** An age in the largest whole unit: `70 s`, `5 min`, `3 h`, `2 d`. */
export function ageText(seconds: number): string {
  if (seconds < 120) return `${seconds} s`
  if (seconds < 7200) return `${Math.floor(seconds / 60)} min`
  if (seconds < 172_800) return `${Math.floor(seconds / 3600)} h`
  return `${Math.floor(seconds / 86_400)} d`
}

interface ChipView {
  tone: 'ok' | 'warn' | 'bad' | 'idle'
  icon: IconName
  text: string
  title: string
  /** What a screen reader hears when the state changes: it never holds the ticking numbers. */
  announce: string
}

/**
 * The snapshot freshness chip (REQ:top-bar): "updated N s ago", amber when the
 * snapshot is older than two refresh intervals, and the number of repositories
 * scanned while the daemon is warming up.
 */
@Component({
  selector: 'app-freshness-chip',
  imports: [Icon],
  template: `<span class="chip" [class]="'chip tone-' + view().tone" [attr.title]="view().title" data-testid="freshness-chip">
    <app-icon [name]="view().icon" />
    <span class="chip-text">{{ view().text }}</span>
  </span>
  <span class="visually-hidden" role="status">{{ view().announce }}</span>`,
  styles: `
    :host {
      display: inline-flex;
      min-width: 0;
    }
    .chip-text {
      overflow: hidden;
      text-overflow: ellipsis;
    }
    /* The text changes with every second and with the warm-up: a fixed width and a fixed start keep the bar from moving. */
    @media (min-width: 48rem) {
      .chip {
        min-width: 9.75rem;
      }
    }
    /* A narrow window keeps the icon; the words stay for assistive technology and in the title. */
    @media (min-width: 48rem) and (max-width: 66rem) {
      .chip {
        min-width: 0;
      }
      .chip-text {
        position: absolute;
        width: 1px;
        height: 1px;
        margin: -1px;
        overflow: hidden;
        clip-path: inset(50%);
        white-space: nowrap;
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FreshnessChip {
  private readonly store = inject(FleetStore)
  private readonly clock = signal(Date.now())

  protected readonly view = computed<ChipView>(() => {
    const store = this.store
    if (store.schemaMismatch() !== null) return { tone: 'bad', icon: 'alert', text: 'not readable', title: 'The daemon speaks another schema version, so no data is shown.', announce: 'The data cannot be read' }
    if (!store.loaded()) return { tone: 'idle', icon: 'clock', text: 'connecting', title: 'Waiting for the daemon.', announce: 'Connecting to the daemon' }
    const document = store.document()
    if (document.warming_up) {
      const { scanned, total } = store.progress()
      return { tone: 'idle', icon: 'refresh', text: `scanned ${scanned} of ${total}`, title: 'The daemon is still scanning repositories; what is listed is complete for those scanned.', announce: 'The daemon is scanning repositories' }
    }
    const taken = Date.parse(document.snapshot_at ?? '')
    if (Number.isNaN(taken)) return { tone: 'idle', icon: 'clock', text: 'snapshot time unknown', title: 'The document carries no snapshot time.', announce: 'The snapshot time is unknown' }
    const age = Math.max(0, Math.floor((this.clock() - taken) / 1000))
    const interval = document.refresh_interval_seconds ?? DEFAULT_REFRESH_SECONDS
    const stale = age > 2 * interval
    return {
      tone: stale ? 'warn' : 'ok',
      icon: stale ? 'alert' : 'check-circle',
      text: `updated ${ageText(age)} ago`,
      announce: stale ? 'The snapshot is out of date' : 'The snapshot is up to date',
      title: stale ? `Older than two refresh intervals of ${interval} s: the daemon is not refreshing.` : `The snapshot is refreshed every ${interval} s.`,
    }
  })

  constructor() {
    const timer = setInterval(() => this.clock.set(Date.now()), CLOCK_TICK_MS)
    inject(DestroyRef).onDestroy(() => clearInterval(timer))
  }
}
