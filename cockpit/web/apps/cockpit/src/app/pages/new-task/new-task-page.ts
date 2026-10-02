import { ChangeDetectionStrategy, Component, computed, effect, inject, signal, untracked } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, Router, RouterLink } from '@angular/router'
import { FleetStore, machineLoad, taskDetailLink } from '@cockpit/fleet-data'
import { buildRepositories } from '@cockpit/fleet-data/list'
import { CopyCommandList, StateBadge } from '@cockpit/ui/control'
import { MetricsPoller, watchMetrics } from '../../metrics/metrics-poller'
import { sampleWords } from '../machines/machine-text'
import { EMPTY_STATE, NewTaskState, commandsOf, defaultBranchOf, machineChoices, modelsOffered, nameProblem, queryOf, stateOf } from './new-task-form'
import { RepositoryPicker } from './repository-picker'

/**
 * The "New task" form (REQ:new-task-form), `/tasks/new`: a repository picker, the task name, an optional base branch, the
 * model, the brief and the machine, and the exact commands to copy: `wb worktree create` once for every repository and
 * `wb agent dispatch` for each, from the library's `newTaskCommands`. Nothing is run. The answers live in the address
 * (shareable, back and forward restore them) except the brief, which stays in memory.
 *
 * Next to each machine choice the load verdict from its metrics (free, busy or unknown) says whether it can take another
 * agent; those metrics are polled while the form is shown.
 */
@Component({
  selector: 'app-new-task-page',
  imports: [RouterLink, RepositoryPicker, CopyCommandList, StateBadge],
  templateUrl: './new-task-page.html',
  styleUrl: './new-task-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class NewTaskPage {
  protected readonly store = inject(FleetStore)
  private readonly router = inject(Router)
  private readonly poller = inject(MetricsPoller)
  private readonly address = toSignal(inject(ActivatedRoute).queryParamMap, { requireSync: true })

  /** The form: what the address said when it last changed from outside, and every edit since. */
  protected readonly state = signal<NewTaskState>(EMPTY_STATE)
  /** The brief: in memory only, never in the address. */
  protected readonly brief = signal('')
  /** The addresses this form wrote and the router has not yet reported back, so that its own edits are not taken for news. */
  private readonly written = new Set<string>()

  protected readonly choices = computed(() => machineChoices(this.store.model()))
  protected readonly names = computed(() => [...new Set(buildRepositories(this.store.model()).map((repository) => repository.slug))])
  protected readonly models = computed(() => modelsOffered(this.store.document()))
  protected readonly nameError = computed(() => nameProblem(this.state().task))
  /** A task of this name exists: a hint, not a stop (a second repository can join a task). */
  protected readonly existing = computed(() => {
    const task = this.store.model().taskNamed(this.state().task)
    return task === undefined ? undefined : { link: taskDetailLink(task.name), worktrees: task.worktrees.length }
  })
  protected readonly defaultBranch = computed(() => defaultBranchOf(this.store.document(), this.state().repositories))
  protected readonly choice = computed(() => this.choices().find((candidate) => candidate.id === this.state().machine) ?? this.choices()[0])
  protected readonly commands = computed(() => commandsOf(this.state(), this.brief(), this.choice().target))

  constructor() {
    // The machines that can be chosen are polled while the form is shown, for the load next to each.
    watchMetrics(() => this.choices().flatMap((choice) => (choice.metricsId === undefined ? [] : [choice.metricsId])))
    effect(() => {
      const next = stateOf(this.address())
      untracked(() => {
        const key = JSON.stringify(next)
        // An address this form wrote is its own edit; any other (back, a pasted link, the top bar's button) is the new form.
        if (this.written.has(key)) {
          if (key === JSON.stringify(this.state())) this.written.clear()
          return
        }
        this.written.clear()
        this.state.set(next)
      })
    })
  }

  /** The load verdict of a machine's latest sample, and for a sample that is not current the words that say how old it is. */
  protected loadOf(metricsId: string | undefined): { state: ReturnType<typeof machineLoad>['state']; stale: string | undefined } {
    const now = this.store.now()
    const load = machineLoad(metricsId === undefined ? undefined : this.poller.metricsOf(metricsId), now)
    return { state: load.state, stale: load.stale ? sampleWords(load, now) : undefined }
  }

  /** One field changed. A text field replaces the current history entry; choosing a repository or a machine is a step back can undo. */
  protected edit(change: Partial<NewTaskState>, step = false): void {
    const next = { ...this.state(), ...change }
    this.state.set(next)
    this.written.add(JSON.stringify(next))
    void this.router.navigate([], { queryParams: queryOf(next), replaceUrl: !step })
  }

  protected add(names: string[]): void {
    const chosen = this.state().repositories
    this.edit({ repositories: [...chosen, ...names.filter((name) => !chosen.includes(name))] }, true)
  }

  protected remove(name: string): void {
    this.edit({ repositories: this.state().repositories.filter((chosen) => chosen !== name) }, true)
  }

  protected value(event: Event): string {
    return (event.target as HTMLInputElement).value
  }
}
