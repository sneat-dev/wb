import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { Machine } from '@cockpit/fleet-data'
import { Glyph } from './glyph'
import { GLYPH_SERVER } from './glyphs'
import { RelativeTime } from './relative-time'
import { StateBadge } from './state-badge'

/**
 * A machine on one line (REQ:machines-filter-and-stale-chip): its name, how it
 * is reached (local, live or cached; with the snapshot's age for a cached one),
 * the transport of a live remote (`http` or `ssh`) and a "stale" mark when the
 * snapshot is older than the freshness window. The mark is a word and a glyph,
 * never only a colour.
 */
@Component({
  selector: 'app-machine-chip',
  imports: [Glyph, RelativeTime, StateBadge],
  template: `
    <span class="name"><app-glyph [paths]="server" />{{ machine().machine }}</span>
    <app-state-badge kind="route" size="small" [value]="route()" />
    @if (machine().route === 'cached') {
      <app-relative-time [at]="machine().observed_at" />
    }
    @if (stale()) {
      <app-state-badge kind="route" size="small" value="stale" hint="The snapshot is older than the freshness window" />
    }
    @if (machine().transport; as transport) {
      <span class="transport" [attr.title]="'Read live over ' + transport">{{ transport }}</span>
    }
  `,
  styles: `
    :host {
      display: inline-flex;
      flex-wrap: wrap;
      gap: var(--space-1) var(--space-2);
      align-items: center;
      min-width: 0;
      font-size: var(--fs-sm);
    }
    .name {
      display: inline-flex;
      gap: var(--space-1);
      align-items: center;
      min-width: 0;
      font-weight: var(--fw-semibold);
      overflow-wrap: anywhere;
    }
    .name app-glyph {
      color: var(--text-3);
    }
    .transport {
      padding: 0 6px;
      border: 1px solid var(--border);
      border-radius: var(--radius-sm);
      color: var(--text-2);
      font-family: var(--font-mono);
      font-size: var(--fs-xs);
      line-height: 1.25rem;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachineChip {
  readonly machine = input.required<Machine>()
  /** Whether the snapshot of a cached machine is stale (the view model decides). */
  readonly stale = input(false)

  protected readonly server = GLYPH_SERVER
  protected readonly route = computed(() => this.machine().route)
}
