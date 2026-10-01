import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { CodeIndex, codeIndexText } from '@cockpit/fleet-data'
import { RelativeTime } from '../control/relative-time'
import { StateBadge } from '../control/state-badge'

/** The code-index freshness of a checkout: one badge per indexer (the state in words, "stale, 3 behind") and the age of its receipt; a dash when not known. */
@Component({
  selector: 'app-code-index-cell',
  imports: [StateBadge, RelativeTime],
  template: `@for (index of states(); track index.indexer) {
      <app-state-badge kind="code-index" size="small" [value]="index.state" [label]="text(index)" />
      @if (index.receipt_at) {
        <app-relative-time [at]="index.receipt_at" />
      }
    } @empty {
      <span class="muted" title="Not known for this entry">—</span>
    }`,
  styles: `
    :host {
      display: flex;
      gap: var(--space-1);
      align-items: center;
      overflow: hidden;
    }
    .muted {
      color: var(--text-3);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CodeIndexCell {
  readonly states = input<readonly CodeIndex[] | undefined>()
  protected readonly text = codeIndexText
}
