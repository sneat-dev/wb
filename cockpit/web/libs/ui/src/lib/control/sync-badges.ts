import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'

interface SyncBadge {
  key: string
  tone: 'warn' | 'bad' | 'idle'
  text: string
  /** The words for assistive technology and the tooltip. */
  name: string
}

/**
 * The sync facts of a worktree's branch (REQ:worktrees-columns-and-badges): `↑n`
 * commits not pushed, `↓n` commits behind, "gone" when the upstream is gone and
 * "no upstream" when the branch has none. They exist only for worktrees of the
 * machine the Cockpit runs on: with none of the facts the component renders
 * nothing at all, so a cached worktree shows no badge and leaves no gap.
 */
@Component({
  selector: 'app-sync-badges',
  template: `@for (badge of badges(); track badge.key) {
    <span [class]="'sync tone-' + badge.tone" [attr.title]="badge.name"
      ><span aria-hidden="true">{{ badge.text }}</span
      ><span class="visually-hidden">{{ badge.name }}</span></span
    >
  }`,
  styles: `
    :host {
      display: inline-flex;
      flex-wrap: wrap;
      gap: var(--space-1);
    }
    :host:empty {
      display: none;
    }
    .sync {
      display: inline-flex;
      align-items: center;
      height: 1.25rem;
      padding: 0 6px;
      border: 1px solid var(--idle-border);
      border-radius: var(--radius-sm);
      background: var(--idle-soft);
      color: var(--idle);
      font-size: var(--fs-xs);
      font-weight: var(--fw-medium);
      font-variant-numeric: tabular-nums;
      line-height: 1;
      white-space: nowrap;
    }
    .tone-warn {
      border-color: var(--warn-border);
      background: var(--warn-soft);
      color: var(--warn);
    }
    .tone-bad {
      border-color: var(--bad-border);
      background: var(--bad-soft);
      color: var(--bad);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SyncBadges {
  /** Commits on the branch that its upstream does not have. */
  readonly ahead = input<number | undefined>()
  readonly behind = input<number | undefined>()
  readonly upstreamGone = input<boolean | undefined>()
  /** `false` is a branch with no upstream; absent is not known. */
  readonly hasUpstream = input<boolean | undefined>()

  protected readonly badges = computed<SyncBadge[]>(() => {
    const badges: SyncBadge[] = []
    const ahead = this.ahead() ?? 0
    const behind = this.behind() ?? 0
    if (ahead > 0) badges.push({ key: 'ahead', tone: 'warn', text: `↑${ahead}`, name: `${ahead} ${ahead === 1 ? 'commit' : 'commits'} not pushed` })
    if (behind > 0) badges.push({ key: 'behind', tone: 'warn', text: `↓${behind}`, name: `${behind} ${behind === 1 ? 'commit' : 'commits'} behind` })
    if (this.upstreamGone() === true) badges.push({ key: 'gone', tone: 'bad', text: 'gone', name: 'upstream branch is gone' })
    if (this.hasUpstream() === false) badges.push({ key: 'none', tone: 'idle', text: 'no upstream', name: 'branch has no upstream' })
    return badges
  })
}
