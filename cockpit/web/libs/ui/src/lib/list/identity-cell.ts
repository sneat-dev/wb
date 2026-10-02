import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { AppLink, linkTarget } from '@cockpit/fleet-data'
import { CopyIcon } from './copy-icon'

/**
 * The first cell of a list: the entity's name in strong type and a muted
 * secondary text right after it (a worktree's repository, a task's repositories),
 * in one row that uses the room the cell has. When the cell is short of room the
 * secondary text shrinks first, then the name; both truncate with an ellipsis and
 * the cell's `title` holds the full text. The name may link to the entity's task or
 * page; a click on it follows the link, a click anywhere else in the row selects
 * the row. A copy icon at the end puts the full name on the clipboard.
 */
@Component({
  selector: 'app-identity-cell',
  imports: [RouterLink, CopyIcon],
  template: `<span class="text">
      @if (link(); as to) {
        <a class="name" tabindex="-1" [routerLink]="target(to)" [queryParams]="to.query">{{ name() }}</a>
      } @else {
        <strong class="name">{{ name() }}</strong>
      }
      @if (secondary()) {
        <span class="secondary">{{ secondary() }}</span>
      }
    </span>
    <app-copy-icon [value]="name()" [label]="copyLabel()" [tabbable]="false" />`,
  styles: `
    :host {
      display: flex;
      gap: var(--space-2);
      align-items: center;
      min-width: 0;
    }
    /* The name and, right after it, the muted text: one row that takes the room it needs and no more. */
    .text {
      display: flex;
      flex: 0 1 auto;
      gap: var(--space-2);
      align-items: baseline;
      min-width: 0;
      overflow: hidden;
    }
    .name,
    .secondary {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    /*
     * The name has the room first: it never shrinks (a shrink of a fraction of a pixel is enough for the browser to
     * draw an ellipsis and cut two letters), it is cut only when it alone is wider than the cell. The muted text
     * takes what is left, down to nothing.
     */
    .name {
      flex: 0 0 auto;
      max-width: 100%;
      color: var(--text);
      font-weight: var(--fw-semibold);
    }
    .secondary {
      flex: 0 1 auto;
      color: var(--text-3);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IdentityCell {
  protected readonly target = linkTarget
  readonly name = input.required<string>()
  readonly secondary = input<string>()
  /** Where the name leads, when it is a link. */
  readonly link = input<AppLink>()
  /** What the copy icon says: "Copy task name". */
  readonly copyLabel = input('Copy name')
}
