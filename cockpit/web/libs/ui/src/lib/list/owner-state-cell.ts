import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { Worktree } from '@cockpit/fleet-data'
import { StateBadge } from '../control/state-badge'
import { SyncBadges } from '../control/sync-badges'

/**
 * A worktree's State cell (REQ:worktrees-list): the owner state as a badge (plain muted text for `idle`) and
 * the sync badges `↑n`, `↓n`, "gone" and "no upstream" beside it. The sync facts
 * exist only for worktrees of this machine, so another machine's gets none.
 */
@Component({
  selector: 'app-owner-state-cell',
  imports: [StateBadge, SyncBadges],
  template: `@if (worktree().owner_state === 'idle') {
      <span class="idle" title="Owner state: idle"><span class="visually-hidden">Owner state: </span>idle</span>
    } @else {
      <app-state-badge kind="owner" size="small" [value]="worktree().owner_state" />
    }
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
    /* Idle is the quietest state: text only, so that the pills of the states that need a look stand out. */
    .idle {
      padding: 0 var(--space-1);
      color: var(--text-3);
      font-size: var(--fs-xs);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class OwnerStateCell {
  readonly worktree = input.required<Worktree>()
}
