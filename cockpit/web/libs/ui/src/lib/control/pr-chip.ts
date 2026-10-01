import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { PullRequest } from '@cockpit/fleet-data'
import { RelativeTime } from './relative-time'
import { StateBadge } from './state-badge'

/** The address of a link the daemon sent, only when it is a web address. */
export function webAddress(url: string | undefined): string | null {
  return url !== undefined && /^https?:\/\/[^\s]+$/i.test(url) ? url : null
}

/**
 * A pull request on one line (REQ:field-tables): its number, its state, how many
 * checks passed, the name of the first failed check (truncated, the full name in
 * the tooltip) and how long ago the daemon observed it (against the shared clock). A field the daemon did
 * not report is said to be unreported, never guessed; a pull request that has
 * never been observed says "not yet checked".
 */
@Component({
  selector: 'app-pr-chip',
  imports: [StateBadge, RelativeTime],
  template: `
    @if (address(); as href) {
      <a class="number" [href]="href" target="_blank" rel="noopener noreferrer" [attr.aria-label]="'Pull request ' + pullRequest().number + ', opens in a new tab'">#{{ pullRequest().number }}</a>
    } @else {
      <span class="number">#{{ pullRequest().number }}</span>
    }
    <app-state-badge kind="pr-state" size="small" [value]="pullRequest().state" />
    <app-state-badge kind="checks" size="small" [value]="checks().value" [label]="checks().label" />
    @if (failedCheck(); as name) {
      <span class="failed" [attr.title]="name">{{ name }}</span>
    }
    @if (pullRequest().checked_at) {
      <span class="observed">checked <app-relative-time [at]="pullRequest().checked_at" /></span>
    } @else {
      <span class="observed">not yet checked</span>
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
    .number {
      font-weight: var(--fw-semibold);
      font-variant-numeric: tabular-nums;
    }
    a.number {
      color: var(--accent);
    }
    .failed {
      max-width: 14ch;
      overflow: hidden;
      color: var(--bad);
      font-family: var(--font-mono);
      font-size: var(--fs-xs);
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .observed {
      color: var(--text-3);
      font-size: var(--fs-xs);
      white-space: nowrap;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PrChip {
  readonly pullRequest = input.required<PullRequest>()

  protected readonly address = computed(() => webAddress(this.pullRequest().url))
  protected readonly failedCheck = computed(() => (failedOf(this.pullRequest()) ? this.pullRequest().failed_check : undefined))
  protected readonly checks = computed(() => checksOf(this.pullRequest()))
}

function failedOf(pr: PullRequest): boolean {
  return (pr.checks_failed ?? 0) > 0
}

/** The verdict and the "passed/total" text of a pull request's checks; the verdict is the daemon's `checks_green`, never re-derived. */
export function checksOf(pr: PullRequest): { value: 'passed' | 'failed' | 'pending' | 'unknown'; label: string | undefined } {
  const label = pr.checks_total === undefined ? undefined : `${pr.checks_passed ?? 0}/${pr.checks_total}`
  if (failedOf(pr)) return { value: 'failed', label }
  if ((pr.checks_pending ?? 0) > 0) return { value: 'pending', label }
  return { value: pr.checks_green === true ? 'passed' : 'unknown', label }
}
