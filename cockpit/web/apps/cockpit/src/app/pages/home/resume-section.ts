import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetModel, TaskView, selectionLink } from '@cockpit/fleet-data'
import { GLYPH_CHECK_CIRCLE, Glyph, RelativeTime, StateBadge } from '@cockpit/ui/control'
import { counted, isoOf } from './home-format'

/**
 * Home "Resume" (REQ:home-resume): the last five tasks by activity, each with its state badge and
 * age. The row is one link ("Open") that selects the task on Tasks.
 */
@Component({
  selector: 'app-resume',
  imports: [RouterLink, Glyph, RelativeTime, StateBadge],
  templateUrl: './resume-section.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResumeSection {
  readonly model = input.required<FleetModel>()

  protected readonly tasks = computed(() => this.model().resume)
  protected readonly idle = GLYPH_CHECK_CIRCLE
  protected readonly iso = isoOf
  protected readonly link = (task: TaskView) => selectionLink('tasks', task.name)
  protected readonly where = (task: TaskView) => counted(task.worktrees.length, 'worktree')
}
