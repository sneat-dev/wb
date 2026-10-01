import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, Router, RouterLink } from '@angular/router'
import { FleetStore, MergedRepository } from '@cockpit/fleet-data'
import { LinkResult, ListRow, buildRepositories, codeBrowserLink, parseListQuery, repositoryAgentsLink, repositoryPullRequestsLink, repositoryWorktreesLink } from '@cockpit/fleet-data/list'
import { Glyph, StateBadge, UiClock } from '@cockpit/ui/control'
import { ADDRESS_KEYS, ALWAYS, AgeText, CopyIcon, ListCell, ListColumn, ListPanelTemplate, ListView, CountLink, RepoName, effectiveSort } from '@cockpit/ui/list'
import { MAX_CHIPS, MachineChipView, machineChips } from './repository-machines'
import { RepositoryPanelView } from './repository-panel'
import { SORT_PRESETS, SortPreset, activePreset } from './repository-sort'

// TODO(ui library): `@cockpit/ui/control` exports no code or external-link glyph (glyphs.ts is not an entry point), so the two the actions cell needs are drawn here.
const GLYPH_CODE = ['m8 8-4 4 4 4', 'm16 8 4 4-4 4', 'm13.5 5-3 14']
const GLYPH_EXTERNAL = ['M14 4h6v6', 'M20 4 10 14', 'M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4']

/** A count cell: not reported (a dash), a quiet zero, or a number that links to the list it counted. */
interface CountCell {
  value?: number
  link: LinkResult
  zero: boolean
}

/** What a row's cells show that is worked out once per row. */
interface RowCells {
  worktrees: CountCell
  agents: CountCell
  prs: CountCell
  /** "5 / 12", a dash for the side that is not reported; empty when neither is. */
  branches: string
  branchesTitle: string
  indexTitle: string
}

const countCell = (value: number | undefined, link: LinkResult): CountCell => ({ value, link, zero: value === 0 })

/**
 * Every repository, one row per repository identity across machines (REQ:repositories-list), with a
 * one-click switch between Recent, Most worktrees and Most branches that sets the list's sort, the
 * counts that open the lists they counted, machine chips that open the panel at that machine's
 * checkout, and icon buttons to browse the code and to open the repository on its host.
 */
@Component({
  selector: 'app-repositories-page',
  imports: [RouterLink, ListView, ListCell, ListPanelTemplate, RepoName, CopyIcon, CountLink, StateBadge, AgeText, Glyph, RepositoryPanelView],
  templateUrl: './repositories-page.html',
  styleUrl: './repositories-page.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoriesPage {
  protected readonly store = inject(FleetStore)
  private readonly router = inject(Router)
  private readonly route = inject(ActivatedRoute)
  private readonly clock = inject(UiClock).now
  private readonly params = toSignal(this.route.queryParamMap, { requireSync: true })

  protected readonly presets = SORT_PRESETS
  protected readonly codeGlyph = GLYPH_CODE
  protected readonly externalGlyph = GLYPH_EXTERNAL

  /** The order the list is in, which a header click may have chosen: the address's sort, or the page's default. */
  protected readonly order = computed(() => {
    const params = this.params()
    return effectiveSort('repositories', parseListQuery('repositories', Object.fromEntries(ADDRESS_KEYS.map((key) => [key, params.get(key) ?? undefined]))))
  })
  protected readonly active = computed(() => activePreset(this.order()))

  /** The machine chips of each row, from the clock (a cached snapshot's age) and the machines (a transport). */
  private readonly chipsByKey = computed(() => {
    const now = this.clock()
    const machines = this.store.model().machines
    const rows = new Map<string, { shown: MachineChipView[]; more: number; moreTitle: string }>()
    for (const row of this.rowsOf()) {
      const chips = machineChips(row.checkouts, machines, now)
      rows.set(row.key, { shown: chips.slice(0, MAX_CHIPS), more: Math.max(0, chips.length - MAX_CHIPS), moreTitle: chips.slice(MAX_CHIPS).map((chip) => chip.name).join(', ') })
    }
    return rows
  })

  private readonly cells = new WeakMap<MergedRepository, RowCells>()

  protected readonly panelLabel = (repository: MergedRepository) => `Repository ${repository.slug}`
  /** A `sel` names the entry id of any checkout, not only the one the row is keyed by. */
  protected readonly resolve = (rows: readonly ListRow<MergedRepository>[], sel: string) => rows.find((row) => row.item.checkouts.some((checkout) => checkout.repository.id === sel))

  protected readonly columns: ListColumn<MergedRepository>[] = [
    { id: 'repository', header: 'Repository', sort: 'repository', width: 'fill', grow: 4, min: 240, priority: ALWAYS, value: (r) => r.slug },
    { id: 'machines', header: 'Machines', width: 'fill', grow: 3, min: 200, priority: 5, value: (r) => r.checkouts.map((checkout) => checkout.machine).join(', '), empty: () => this.store.document().machines.length <= 1 },
    { id: 'worktrees', header: 'Worktrees', sort: 'worktrees', width: 96, min: 88, priority: 4, align: 'end', value: (r) => String(r.worktreeCount) },
    { id: 'branches', header: 'Branches', sort: 'branches', width: 110, min: 100, priority: 4, align: 'end', hint: 'Local / remote. These counts do not link: there is no branches list page', value: (r) => this.cellsOf(r).branches },
    { id: 'agents', header: 'Agents', width: 80, min: 72, priority: 2, align: 'end', hint: 'Running agents', value: (r) => (r.activeAgentCount ? String(r.activeAgentCount) : '') },
    { id: 'prs', header: 'PRs', width: 64, min: 56, priority: 2, align: 'end', hint: 'Open pull requests', value: (r) => (r.openPullRequestCount ? String(r.openPullRequestCount) : '') },
    { id: 'index', header: 'Code index', width: 120, min: 110, priority: 3, hint: 'The worst code-index state across machines', value: (r) => r.codeIndex },
    { id: 'activity', header: 'Last activity', sort: 'activity', width: 104, min: 96, priority: ALWAYS },
    { id: 'links', header: 'Links', width: 72, min: 72, priority: ALWAYS, hint: 'Browse the code, open on the host' },
  ]

  private rowsOf() {
    return buildRepositories(this.store.model())
  }

  protected chipsOf(repository: MergedRepository) {
    return this.chipsByKey().get(repository.key)
  }

  protected cellsOf(repository: MergedRepository): RowCells {
    let cells = this.cells.get(repository)
    if (cells === undefined) {
      const { localBranchCount: local, remoteBranchCount: remote } = repository
      const machines = repository.checkouts.length
      cells = {
        worktrees: countCell(repository.worktreeCount, repositoryWorktreesLink(repository.slug)),
        agents: countCell(repository.activeAgentCount, repositoryAgentsLink(repository.slug)),
        prs: countCell(repository.openPullRequestCount, repositoryPullRequestsLink(repository.slug)),
        branches: local === undefined && remote === undefined ? '' : `${local ?? '—'} / ${remote ?? '—'}`,
        branchesTitle: `${local ?? 'not reported'} local, ${remote ?? 'not reported'} remote. Not a link: there is no branches list page`,
        indexTitle: `The worst code-index state across ${machines} ${machines === 1 ? 'machine' : 'machines'}`,
      }
      this.cells.set(repository, cells)
    }
    return cells
  }

  /** The repository in the code browser, when one is configured; none otherwise (REQ:repositories-list). */
  protected codeLink(repository: MergedRepository): string | null {
    return codeBrowserLink(this.store.codeBrowserUrl(), { ...repository.checkouts[0].repository, host: repository.host, name: repository.slug })
  }

  /** A click on a preset sets the sort (newest or largest first) in the address, where the list reads it and back and forward restore it. */
  protected choose(preset: SortPreset['id']): void {
    void this.router.navigate([], { relativeTo: this.route, queryParams: { sort: preset, dir: 'desc' }, queryParamsHandling: 'merge' })
  }
}
