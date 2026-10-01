import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { PullRequest, isOpenPullRequest, webAddress } from '@cockpit/fleet-data'
import { StateBadge, checksOf } from '@cockpit/ui/control'

/** How many pull requests a Tasks row names before "+n". */
export const PULL_REQUESTS_SHOWN = 2

interface Shown {
  pr: PullRequest
  address: string | undefined
  /** `checks` for an open pull request that was observed, `state` for one that merged or closed, none for one never observed. */
  kind: 'checks' | 'state' | 'unobserved'
  checks: ReturnType<typeof checksOf>
}

/**
 * The pull requests of a task in the least room (REQ:tasks-list): for each of the first two, open ones first,
 * its number (a link only for a checked web address) and its checks passed over total, or its state when it
 * merged or closed, or "not checked" when it was never observed; then "+n" for the rest. The panel has the
 * full chips.
 */
@Component({
  selector: 'app-task-pr-cell',
  imports: [StateBadge],
  template: `@for (item of shown(); track item.pr.id) {
      <span class="pr">
        @if (item.address; as href) {
          <a class="number" tabindex="-1" [href]="href" target="_blank" rel="noopener noreferrer" [attr.aria-label]="'Pull request ' + item.pr.number + ', opens in a new tab'">#{{ item.pr.number }}</a>
        } @else {
          <span class="number">#{{ item.pr.number }}</span>
        }
        @switch (item.kind) {
          @case ('checks') {
            <app-state-badge kind="checks" size="small" [value]="item.checks.value" [label]="item.checks.label" />
          }
          @case ('state') {
            <app-state-badge kind="pr-state" size="small" [value]="item.pr.state" />
          }
          @default {
            <span class="unobserved" title="Not yet checked">not checked</span>
          }
        }
      </span>
    }
    @if (more() > 0) {
      <span class="more" [attr.title]="more() + ' more pull requests'">+{{ more() }}</span>
    }`,
  styles: `
    :host {
      display: flex;
      flex-wrap: nowrap;
      gap: var(--space-2);
      align-items: center;
      overflow: hidden;
    }
    .pr {
      display: inline-flex;
      flex: none;
      gap: var(--space-1);
      align-items: center;
    }
    .number {
      font-size: var(--fs-sm);
      font-weight: var(--fw-semibold);
      font-variant-numeric: tabular-nums;
    }
    a.number {
      color: var(--accent);
    }
    .unobserved,
    .more {
      color: var(--text-3);
      font-size: var(--fs-xs);
      white-space: nowrap;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TaskPrCell {
  readonly pullRequests = input.required<readonly PullRequest[]>()

  /** Open pull requests first, in the order the document lists them. */
  private readonly ordered = computed(() => [...this.pullRequests().filter(isOpenPullRequest), ...this.pullRequests().filter((pr) => !isOpenPullRequest(pr))])

  protected readonly shown = computed<Shown[]>(() =>
    this.ordered()
      .slice(0, PULL_REQUESTS_SHOWN)
      .map((pr) => ({
        pr,
        address: webAddress(pr.url),
        kind: pr.checked_at === undefined ? 'unobserved' : pr.state === 'merged' || pr.state === 'closed' ? 'state' : 'checks',
        checks: checksOf(pr),
      })),
  )
  protected readonly more = computed(() => Math.max(0, this.pullRequests().length - PULL_REQUESTS_SHOWN))
}
