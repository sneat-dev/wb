import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { CopyCommand } from '@cockpit/fleet-data'
import { ActionSlot, SlotCopy, copyLabel } from '@cockpit/ui/control'
import { HomeRegistry, PUSH_ACTION } from './home-registry'
import { WorkOffer } from './needs-you-rows'

/** `wb pr create '<task>' --commit-all --message=<<<edit:message>>>`: the library's template, loaded when the button is pressed. */
const commitAndOpen = (task: string) => async (): Promise<CopyCommand> => (await import('@cockpit/fleet-data/commands')).pullRequestCreate(task)

/**
 * The secondary action of a work-at-risk row (REQ:home-needs-you), after its primary "Open task": one action slot.
 * No page handles the registry's `branch.push` yet, so the slot is what it is without a handler: one quiet icon
 * button "Copy template" with the `wb pr create` command for the task. (Once a handler exists the slot draws the
 * registry's Push for the first worktree it is offered for.) It is a lazy chunk because it depends on the registry's answers.
 */
@Component({
  selector: 'app-work-action',
  imports: [ActionSlot],
  template: `<app-action-slot [actions]="offered()" [target]="target()" [copy]="copy()" />`,
  styles: ':host { display: contents; }',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkAction {
  readonly action = input.required<WorkOffer>()

  private readonly registry = inject(HomeRegistry)
  /** The worktree the registry offers a push for, the first one; else the first worktree (a row at risk has one). */
  private readonly pushable = computed(() => this.action().worktrees.find((worktree) => this.registry.offered(`worktree:${worktree.id}`, PUSH_ACTION) !== undefined) ?? this.action().worktrees[0])
  /** The repositories of the work at risk, once each: a task of two repositories says so in the name of its button. */
  private readonly repositories = computed(() => [...new Set(this.action().worktrees.map((worktree) => worktree.repository))])
  protected readonly target = computed(() => `worktree:${this.pushable().id}`)
  protected readonly offered = computed(() => this.registry.offered(this.target(), PUSH_ACTION))
  protected readonly copy = computed<SlotCopy>(() => ({
    build: commitAndOpen(this.action().task),
    label: copyLabel(true, 'wb pr create', `commit everything and open the pull request${this.repositories().length > 1 ? ` of ${this.repositories().join(' and ')}` : ''}`),
    template: true,
    quiet: true,
  }))
}
