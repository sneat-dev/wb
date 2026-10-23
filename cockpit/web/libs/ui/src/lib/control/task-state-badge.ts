import { ChangeDetectionStrategy, Component, ViewEncapsulation, computed, input } from '@angular/core'
import { Glyph } from './glyph'
import { taskSpec } from './state-tables/task'

/**
 * The state badge of a task and nothing else: the same markup and look as `StateBadge` with kind
 * `task`, but it carries the task table alone, not the tables of every kind, so a page whose first
 * paint must stay small (Home's "Needs you") ships a fraction of the code.
 */
@Component({
  selector: 'app-task-state-badge',
  imports: [Glyph],
  template: `<span [class]="'badge tone-' + spec().tone" [class.small]="size() === 'small'" [class.unreported]="spec().unreported">
    <app-glyph [paths]="spec().icon" />
    <span class="visually-hidden">Task state: {{ spec().unrecognised ? 'not recognised' : '' }}</span>
    <span class="text" [attr.aria-hidden]="spec().unrecognised ? 'true' : null">{{ spec().label }}</span>
  </span>`,
  // Its look is `task-state-badge.css`, which the app includes in its global styles (see there).
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TaskStateBadge {
  readonly value = input<string | undefined>()
  /** `normal` is 24 px high, `small` 20 px (rows of a list). */
  readonly size = input<'normal' | 'small'>('normal')

  protected readonly spec = computed(() => taskSpec(this.value()))
}
