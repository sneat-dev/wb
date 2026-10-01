import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { LinkResult } from '@cockpit/fleet-data'

/**
 * A count cell (REQ:every-number-is-a-link). Given the `link` of the fleet-data
 * link helpers it is a link to the list that produced the number; a number whose
 * name cannot be written as a link, or which is declared non-linking, is plain
 * text with the reason as its `title`. No count means a dash.
 */
@Component({
  selector: 'app-count-link',
  imports: [RouterLink],
  template: `@if (value() === undefined) {
      <span class="muted">—</span>
    } @else if (target(); as to) {
      <a class="tnum" [routerLink]="to.path" [queryParams]="to.query">{{ value() }}</a>
    } @else {
      <span class="tnum" [attr.title]="reason()">{{ value() }}</span>
    }`,
  styles: `
    .muted {
      color: var(--text-3);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CountLink {
  readonly value = input<number | undefined>()
  /** The helper's result; leave it out for a count that never links and give its `reason`. */
  readonly link = input<LinkResult>()
  /** Why the number is plain text, when `link` does not say. */
  readonly why = input<string>()

  protected readonly target = computed(() => {
    const link = this.link()
    return link?.ok ? link.link : undefined
  })
  protected readonly reason = computed(() => {
    const link = this.link()
    return (link && !link.ok ? link.reason : this.why()) ?? null
  })
}
