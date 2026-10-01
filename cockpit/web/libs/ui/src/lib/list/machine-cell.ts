import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { FleetStore, formatAge } from '@cockpit/fleet-data'
import { UiClock } from '../control/ui-clock'

/** The age of a snapshot in the least room: `5 m`, `3 h`, `2 d`; empty when the time is unreadable. */
export function compactAge(observedAt: string | undefined, now: number): string {
  const time = Date.parse(observedAt ?? '')
  if (Number.isNaN(time)) return ''
  const minutes = Math.max(0, Math.floor((now - time) / 60_000))
  if (minutes < 60) return `${minutes} m`
  if (minutes < 24 * 60) return `${Math.floor(minutes / 60)} h`
  return `${Math.floor(minutes / (24 * 60))} d`
}

/**
 * A row's machine, as little as says it: the name alone for this machine, and for a machine read from
 * a cache or live over a transport the name and one chip with what differs: the snapshot's age, "stale"
 * and the transport ("vm · 19 m · ssh"). The name and the chip never wrap; the details are in the `title`.
 */
@Component({
  selector: 'app-machine-cell',
  template: `@if (view(); as machine) {
    <span class="unit" [attr.title]="title()">
      <span class="name">{{ machine.machine.machine }}</span>
      @if (details().length > 0) {
        <span class="chip" [class.stale]="machine.state === 'stale'">{{ details().join(' · ') }}</span>
      }
    </span>
  }`,
  styles: `
    :host {
      display: flex;
      min-width: 0;
      overflow: hidden;
    }
    .unit {
      display: inline-flex;
      flex-wrap: nowrap;
      gap: var(--space-1);
      align-items: center;
      white-space: nowrap;
    }
    .name {
      flex: none;
      font-weight: var(--fw-semibold);
    }
    .chip {
      flex: none;
      padding: 0 6px;
      border: 1px solid var(--border);
      border-radius: var(--radius-sm);
      color: var(--text-2);
      font-size: var(--fs-xs);
      line-height: 1.25rem;
    }
    .chip.stale {
      border-color: var(--warn-border);
      background: var(--warn-soft);
      color: var(--warn);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachineCell {
  /** The machine entry id. */
  readonly id = input.required<string>()
  private readonly store = inject(FleetStore)
  private readonly clock = inject(UiClock).now
  protected readonly view = computed(() => this.store.model().machines.find((machine) => machine.machine.id === this.id()))

  /** What differs from this machine, in order: age (cached), stale, transport. */
  protected readonly details = computed(() => {
    const machine = this.view()
    if (machine === undefined || machine.machine.route === 'local') return []
    return [machine.machine.route === 'cached' ? compactAge(machine.machine.observed_at, this.clock()) : '', machine.state === 'stale' ? 'stale' : '', machine.machine.transport ?? ''].filter((part) => part !== '')
  })

  protected readonly title = computed(() => {
    const machine = this.view()?.machine
    if (machine === undefined || machine.route === 'local') return null
    const how = machine.route === 'cached' ? `Cached snapshot, ${formatAge(machine.observed_at, this.clock())}` : 'Read live'
    return `${machine.machine}: ${how}${machine.transport ? ` over ${machine.transport}` : ''}${this.view()?.state === 'stale' ? '; older than the freshness window' : ''}`
  })
}
