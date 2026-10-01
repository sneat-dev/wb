import { ChangeDetectionStrategy, Component, inject } from '@angular/core'
import { FleetStore, Worktree, taskDetailLink } from '@cockpit/fleet-data'
import { ALWAYS, AgeText, CodeIndexCell, CopyIcon, IdentityCell, ListCell, ListColumn, ListPanelTemplate, ListView, MachineCell, OwnerStateCell, PrCell, RepositoryNames } from '@cockpit/ui/list'
import { WorktreePanelView } from './worktree-panel'

/** Every WB task worktree on every machine (REQ:worktrees-list): the reference list page. */
@Component({
  selector: 'app-worktrees-page',
  imports: [ListView, ListCell, ListPanelTemplate, IdentityCell, OwnerStateCell, PrCell, MachineCell, CodeIndexCell, AgeText, CopyIcon, WorktreePanelView],
  templateUrl: './worktrees-page.html',
  styleUrl: './worktrees-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreesPage {
  protected readonly store = inject(FleetStore)
  private readonly names = inject(RepositoryNames)

  protected readonly repoName = (worktree: Worktree) => this.names.of(worktree.repository).slug
  protected readonly taskLink = (worktree: Worktree) => taskDetailLink(worktree.task)
  protected readonly panelLabel = (worktree: Worktree) => `Worktree ${worktree.task}`
  protected readonly pullRequests = (worktree: Worktree) => this.store.model().worktreePullRequests.get(worktree.id) ?? []

  /** Narrow lists (a panel beside, a tablet) lose Code index and Branch first, then PR, then Machine; Worktree, State and Last activity stay. */
  protected readonly columns: ListColumn<Worktree>[] = [
    { id: 'worktree', header: 'Worktree', sort: 'worktree', width: 'fill', grow: 4, min: 300, priority: ALWAYS, value: (w) => `${w.task} · ${this.repoName(w)}` },
    { id: 'branch', header: 'Branch', width: 'fill', grow: 1, min: 120, priority: 1, value: (w) => w.branch, empty: (w) => w.branch === w.task },
    { id: 'machine', header: 'Machine', sort: 'machine', width: 160, min: 140, priority: 3, value: (w) => w.machine, empty: () => this.store.document().machines.length <= 1 },
    { id: 'state', header: 'State', sort: 'state', width: 250, min: 230, priority: ALWAYS, hint: 'Owner state. ↑ unpushed, ↓ behind and gone are known for this machine only', value: (w) => w.owner_state ?? 'unknown' },
    { id: 'pr', header: 'PR', width: 200, min: 140, priority: 2, empty: (w) => (this.store.model().worktreePullRequests.get(w.id) ?? []).length === 0 },
    { id: 'index', header: 'Code index', width: 160, min: 120, priority: 1, empty: (w) => !w.code_index?.length },
    { id: 'activity', header: 'Last activity', sort: 'activity', width: 104, min: 96, priority: ALWAYS },
  ]
}
