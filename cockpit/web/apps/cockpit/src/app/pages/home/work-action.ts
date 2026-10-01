import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { CopyCommand } from '@cockpit/fleet-data'
import { ActionSlot } from '@cockpit/ui/control'
import { HomeRegistry, PUSH_ACTION } from './home-registry'
import { LazyCopy } from './lazy-copy'
import { WorkOffer } from './needs-you-rows'

/** `wb pr create '<task>' --commit-all --message=<<<edit:message>>>`: the library's template, loaded when the button is pressed. */
const commitAndOpen = (task: string) => async (): Promise<CopyCommand> => (await import('@cockpit/fleet-data/commands')).pullRequestCreate(task)

/**
 * The secondary action of a work-at-risk row (REQ:home-needs-you), after its primary "Open task": where the
 * registry offers `branch.push` for a worktree, an action slot with it ("Push"); otherwise one quiet icon button
 * "Copy template" with the `wb pr create` command for the task. Where neither the registry nor the library has a command,
 * nothing is shown. It is a lazy chunk because it depends on the registry's answers.
 */
@Component({
  selector: 'app-work-action',
  imports: [ActionSlot, LazyCopy],
  template: `@for (worktree of pushable(); track worktree.id) {
      <span class="home-slot" role="group" [attr.aria-label]="worktree.branch"><app-action-slot [actions]="offered(worktree.id)" [target]="'worktree:' + worktree.id" /></span>
    }
    @if (copyable()) {
      <app-lazy-copy idleWord="Copy template" label="Copy command template: commit everything and open the pull request" [build]="build()" [quiet]="true" [iconOnly]="true" />
    }`,
  styles: ':host { display: contents; }',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkAction {
  readonly action = input.required<WorkOffer>()

  private readonly registry = inject(HomeRegistry)
  protected readonly pushable = computed(() => this.action().worktrees.filter((worktree) => this.offered(worktree.id) !== undefined))
  protected readonly copyable = computed(() => this.pushable().length < this.action().worktrees.length)
  protected readonly build = computed(() => commitAndOpen(this.action().task))

  protected offered(id: string) {
    return this.registry.offered(`worktree:${id}`, PUSH_ACTION)
  }
}
