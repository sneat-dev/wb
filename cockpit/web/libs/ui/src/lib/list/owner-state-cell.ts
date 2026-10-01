import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { Worktree } from '@cockpit/fleet-data'
import { StateBadge } from '../control/state-badge'
import { SyncBadges } from '../control/sync-badges'

/**
 * A worktree's State cell (REQ:worktrees-list): the owner state as a badge and
 * the sync badges `↑n`, `↓n`, "gone" and "no upstream" beside it. The sync facts
 * exist only for worktrees of this machine, so another machine's gets none.
 */
@Component({
  selector: 'app-owner-state-cell',
  imports: [StateBadge, SyncBadges],
  template: `<app-state-badge kind="owner" size="small" [value]="worktree().owner_state" />
    @if (worktree().route === 'local') {
      <app-sync-badges [ahead]="worktree().ahead" [behind]="worktree().behind" [upstreamGone]="worktree().upstream_gone" [hasUpstream]="worktree().has_upstream" />
    }`,
  styles: `
    :host {
      display: flex;
      flex-wrap: nowrap;
      gap: var(--space-1);
      align-items: center;
      overflow: hidden;
    }
    :host app-sync-badges {
      flex-wrap: nowrap;
    }
    app-state-badge {
      flex: none;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class OwnerStateCell {
  readonly worktree = input.required<Worktree>()
}
