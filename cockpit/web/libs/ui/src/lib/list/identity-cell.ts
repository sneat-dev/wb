import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { AppLink } from '@cockpit/fleet-data'
import { CopyIcon } from './copy-icon'

/**
 * The first cell of a list: the entity's name in strong type and a muted
 * secondary text after it (a worktree's repository, a task's repositories). The
 * name may link to the entity's task or page; a click on it follows the link,
 * a click anywhere else in the row selects the row. A copy icon puts the full
 * name on the clipboard. Both truncate with an ellipsis; the cell's `title`
 * holds the full text.
 */
@Component({
  selector: 'app-identity-cell',
  imports: [RouterLink, CopyIcon],
  template: `@if (link(); as to) {
      <a class="name" tabindex="-1" [routerLink]="to.path" [queryParams]="to.query">{{ name() }}</a>
    } @else {
      <strong class="name">{{ name() }}</strong>
    }
    <app-copy-icon [value]="name()" [label]="copyLabel()" [tabbable]="false" />
    @if (secondary()) {
      <span class="secondary">{{ secondary() }}</span>
    }`,
  styles: `
    :host {
      display: flex;
      gap: var(--space-2);
      align-items: center;
      min-width: 0;
    }
    .name,
    .secondary {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    /* The name keeps about 22 characters before it truncates; the muted secondary text truncates first. */
    .name {
      flex: 0 1 auto;
      min-width: min(22ch, 100%);
      color: var(--text);
      font-weight: var(--fw-semibold);
    }
    .secondary {
      flex: 0 100 auto;
      color: var(--text-3);
    }
    /* On a phone the name is what fits; the secondary text is in the panel. */
    @container (max-width: 40rem) {
      .secondary {
        display: none;
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IdentityCell {
  readonly name = input.required<string>()
  readonly secondary = input<string>()
  /** Where the name leads, when it is a link. */
  readonly link = input<AppLink>()
  /** What the copy icon says: "Copy task name". */
  readonly copyLabel = input('Copy name')
}
