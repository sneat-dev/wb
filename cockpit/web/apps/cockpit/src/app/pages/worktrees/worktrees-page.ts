import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore, PullRequest, Worktree, taskDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { CodeIndexLabel } from '@cockpit/ui/code-index-label'
import { AgeText, CopyButton, ListCell, ListChip, ListColumn, ListPanelTemplate, ListView, RepositoryNames, provideListShortcuts } from '@cockpit/ui/list'
import { RouteLabel } from '@cockpit/ui/route-label'
import { Shortcuts } from '../../shortcuts/shortcuts'
import { WorktreePanelView } from './worktree-panel'

/** The quick filters of REQ:filter-vocabulary; `unpushed` and `gone` are facts of this machine's checkouts. */
const CHIPS: ListChip[] = [
  { id: 'active', label: 'Active' },
  { id: 'orphaned', label: 'Orphaned' },
  { id: 'unpushed', label: 'Unpushed', hint: 'Commits not pushed; known for this machine only' },
  { id: 'gone', label: 'Upstream gone', hint: 'The upstream branch vanished; known for this machine only' },
  { id: 'pr', label: 'Pull request' },
  { id: 'idle30', label: 'Idle 30 d+', hint: 'No activity for more than 30 days' },
  { id: 'safe', label: 'Safe to clean', hint: 'Home’s cleanup: worktrees that are safe to remove' },
  { id: 'look', label: 'Look first', hint: 'Home’s cleanup: worktrees to look at before removing' },
]

/** The chip tone of an owner state; a placeholder until the shared state badge exists. */
const TONES: Record<string, string> = { active: 'tone-ok', orphaned: 'tone-bad', idle: '', unknown: 'tone-neutral' }

/** Every WB task worktree on every machine (REQ:worktrees-list): the reference list page. */
@Component({
  selector: 'app-worktrees-page',
  imports: [RouterLink, ListView, ListCell, ListPanelTemplate, AgeText, CopyButton, CodeIndexLabel, RouteLabel, WorktreePanelView],
  templateUrl: './worktrees-page.html',
  styleUrl: './worktrees-page.css',
  providers: [provideListShortcuts(Shortcuts)],
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreesPage {
  protected readonly store = inject(FleetStore)
  private readonly names = inject(RepositoryNames)
  /** The legacy `?repository=<entry id>` that the Repositories page's counts still link with, until task 16 links with `repo:"…"`; then this goes. */
  readonly repository = input<string>()
  protected readonly rows = computed(() => {
    const rows = this.store.model().worktreeRows
    const repository = this.repository()
    return repository === undefined ? rows : rows.filter((row) => row.item.repository === repository)
  })
  protected readonly chips = CHIPS
  protected readonly tone = (state: string | undefined) => TONES[state ?? 'unknown']
  protected readonly repoName = (worktree: Worktree) => this.names.of(worktree.repository).slug
  protected readonly taskLink = (worktree: Worktree) => taskDetailLink(worktree.task)
  protected readonly pageLink = (worktree: Worktree) => worktreeDetailLink(worktree.id).path
  protected readonly panelLabel = (worktree: Worktree) => `Worktree ${worktree.task}`

  /** Each worktree's pull request, joined as the `pr` chip joins them. */
  protected readonly pullRequests = computed(() => {
    const model = this.store.model()
    const found = new Map<string, PullRequest>()
    for (const pullRequest of model.document.pull_requests) {
      const task = pullRequest.worktree === undefined ? model.taskOfPullRequest(pullRequest) : undefined
      for (const worktree of model.document.worktrees) {
        if (pullRequest.worktree === worktree.id || (task === worktree.task && pullRequest.repository === worktree.repository && pullRequest.branch === worktree.branch)) found.set(worktree.id, pullRequest)
      }
    }
    return found
  })

  protected readonly columns: ListColumn<Worktree>[] = [
    { id: 'worktree', header: 'Worktree', sort: 'worktree', width: 'fill', grow: 3, min: 132, text: (w) => `${w.task} · ${this.repoName(w)}` },
    { id: 'branch', header: 'Branch', width: 'fill', grow: 2, empty: (w) => w.branch === w.task || w.branch === `task/${w.task}`, text: (w) => w.branch, keep: 4, drop: 'phone' },
    { id: 'machine', header: 'Machine', sort: 'machine', width: 96, text: (w) => w.machine, drop: 'phone' },
    { id: 'source', header: 'Source', width: 130, empty: (w) => w.route === 'local', keep: 1, drop: 'narrow' },
    { id: 'state', header: 'State', sort: 'state', width: 200, hint: 'Owner state. ↑ unpushed, ↓ behind and gone are known for this machine only' },
    { id: 'lifecycle', header: 'Lifecycle', width: 100, empty: (w) => w.lifecycle === undefined, text: (w) => w.lifecycle, keep: 2, drop: 'narrow' },
    { id: 'pr', header: 'PR', width: 96, empty: (w) => !this.pullRequests().has(w.id), drop: 'phone' },
    { id: 'index', header: 'Code index', width: 190, empty: (w) => !w.code_index?.length, drop: 'narrow' },
    { id: 'activity', header: 'Last activity', sort: 'activity', width: 104, min: 96, keep: 6 },
  ]
}
