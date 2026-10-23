import { ChangeDetectionStrategy, Component, input } from '@angular/core'

/**
 * The header of a panel that has a state: its badges (projected: `<app-state-badge>`s) and, in one line of plain
 * words, why it is in that state; anything else the header must say (who reported it, a caveat) goes in the
 * `[panelNote]` slot, under the line. It goes in the `[panelHeader]` slot of `app-panel-content`, so the task
 * panel and the agent panel have the one header:
 *
 * ```html
 * <app-panel-content kind="Task" heading="fix-ci" ...>
 *   <app-panel-state panelHeader [reason]="reason()"><app-state-badge kind="task" value="ready" /></app-panel-state>
 * </app-panel-content>
 * ```
 */
@Component({
  selector: 'app-panel-state',
  template: `<section class="state" aria-label="State">
    <ng-content />
    <p class="why">{{ reason() }}</p>
    <ng-content select="[panelNote]" />
  </section>`,
  styles: `
    :host {
      display: block;
      min-width: 0;
    }
    .state {
      display: flex;
      flex-wrap: wrap;
      gap: var(--space-2) var(--space-3);
      align-items: center;
      padding: var(--space-3);
      border: 1px solid var(--border);
      border-radius: var(--radius-md);
      background: var(--surface-sunken);
    }
    .why {
      flex: 1 1 14rem;
      min-width: 0;
      margin: 0;
      overflow-wrap: anywhere;
      line-height: var(--lh-normal);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PanelState {
  /** Why it is in that state, in one plain sentence. */
  readonly reason = input.required<string>()
}
