import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { PullRequest } from '@cockpit/fleet-data'
import { PrChip } from '../control/pr-chip'

/** The pull request of a row: the control surface's chip for the first one, and how many more there are. */
@Component({
  selector: 'app-pr-cell',
  imports: [PrChip],
  template: `@if (pullRequests()[0]; as first) {
      <app-pr-chip [pullRequest]="first" />
      @if (pullRequests().length > 1) {
        <span class="more" [attr.title]="pullRequests().length + ' pull requests'">+{{ pullRequests().length - 1 }}</span>
      }
    }`,
  styles: `
    :host {
      display: flex;
      gap: var(--space-1);
      align-items: center;
      overflow: hidden;
    }
    /* The chip keeps its own width and the cell clips it, so its badges are cut at the edge, never squeezed into each other. */
    app-pr-chip {
      flex: none;
      flex-wrap: nowrap;
    }
    .more {
      color: var(--text-3);
      font-size: var(--fs-xs);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PrCell {
  readonly pullRequests = input.required<readonly PullRequest[]>()
}
