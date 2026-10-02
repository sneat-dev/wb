import { ChangeDetectionStrategy, Component, computed, effect, inject, input, viewChildren } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute } from '@angular/router'
import { FleetStore, MergedRepository, Worktree, agentDetailLink, agentTitle } from '@cockpit/fleet-data'
import { LinkResult, codeBrowserLink, repositoryAgentsLink, repositoryPullRequestsLink, repositoryWorktreesLink } from '@cockpit/fleet-data/list'
import { RepositoryPanel, buildRepositoryPanel } from '@cockpit/fleet-data/panel'
import { PanelContent, PanelFact, PanelRelated } from '@cockpit/ui/panel'
import { ReadmeSection } from '../../readme/readme-section'
import { RepositoryMachineSection } from './repository-machine-section'

const NOT_REPORTED = 'No machine reports this'

/** A count of the panel: not reported, a quiet zero, or a number that is a link to the list that produced it (REQ:every-number-is-a-link). */
function countFact(label: string, value: number | undefined, link: LinkResult): PanelFact {
  if (value === undefined) return { label, text: 'not reported', muted: true, title: NOT_REPORTED }
  if (value === 0) return { label, text: '0', muted: true }
  return link.ok ? { label, text: String(value), link: link.link } : { label, text: String(value), title: link.reason }
}

/**
 * One repository's content, merged across machines: the side panel of the Repositories list and the
 * page of `/repositories/:host/:owner/:name` are both this component (REQ:detail-routes-share-the-panel).
 * It is built from `buildRepositoryPanel(model, key)`: the merged header (facts and links), one
 * section per machine (its worktrees, code index and lazily read branches), the README for an owner,
 * the Copy-command entries of the library and the Raw data block.
 */
@Component({
  selector: 'app-repository-panel',
  imports: [PanelContent, RepositoryMachineSection, ReadmeSection],
  templateUrl: './repository-panel.html',
  styleUrl: './repository-panel.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoryPanelView {
  /** The merged repository's key: its lower-cased `owner/name`. */
  readonly repositoryKey = input.required<string>()
  /** The detail page, not the side panel. */
  readonly page = input(false)

  protected readonly store = inject(FleetStore)
  private readonly fragment = toSignal(inject(ActivatedRoute).fragment, { requireSync: true })
  private readonly sections = viewChildren(RepositoryMachineSection)
  private revealed = ''

  protected readonly view = computed<RepositoryPanel | undefined>(() => buildRepositoryPanel(this.store.model(), this.repositoryKey()))

  /** The worktrees of each checkout, by its entry id. */
  protected readonly worktreesOf = computed(() => {
    const byCheckout = new Map<string, Worktree[]>()
    for (const worktree of (this.view() as RepositoryPanel).related.worktrees) byCheckout.set(worktree.repository, [...(byCheckout.get(worktree.repository) ?? []), worktree])
    return byCheckout
  })

  protected readonly machines = computed(() => this.store.model().machines)

  protected readonly facts = computed<PanelFact[]>(() => {
    const summary = (this.view() as RepositoryPanel).summary
    const branches: PanelFact =
      summary.localBranchCount === undefined && summary.remoteBranchCount === undefined
        ? { label: 'Branches', text: 'not reported', muted: true, title: NOT_REPORTED }
        : { label: 'Branches', text: `${summary.localBranchCount ?? '—'} local / ${summary.remoteBranchCount ?? '—'} remote`, title: 'Branch counts do not link: there is no branches list page' }
    const facts: PanelFact[] = []
    if (summary.host) facts.push({ label: 'Host', text: summary.host })
    facts.push(
      { label: 'Machines', text: summary.checkouts.map((checkout) => checkout.machine).join(', ') },
      countFact('Worktrees', summary.worktreeCount, repositoryWorktreesLink(summary.slug)),
      branches,
      countFact('Running agents', summary.activeAgentCount, repositoryAgentsLink(summary.slug)),
      countFact('Open pull requests', summary.openPullRequestCount, repositoryPullRequestsLink(summary.slug)),
      summary.codeIndex === undefined ? { label: 'Code index', text: 'not reported', muted: true, title: NOT_REPORTED } : { label: 'Code index', text: summary.codeIndex },
      summary.lastActivityAt === undefined ? { label: 'Last activity', text: 'not reported', muted: true, title: NOT_REPORTED } : { label: 'Last activity', time: summary.lastActivityAt, text: '' },
    )
    for (const error of summary.errors) facts.push({ label: 'Scan error', text: error })
    return facts
  })

  protected readonly related = computed<PanelRelated[]>(() => {
    const { summary, related } = this.view() as RepositoryPanel
    const code = codeBrowserLink(this.store.codeBrowserUrl(), this.primaryEntry(summary))
    const links = [
      ...(summary.webUrl === undefined ? [] : [{ text: `Open on ${summary.host ?? 'the host'}`, href: summary.webUrl }]),
      ...(code === null ? [] : [{ text: 'Browse code', href: code }]),
    ]
    const groups: PanelRelated[] = [
      { title: 'Pull requests', items: related.pullRequests.map((pr) => ({ text: `#${pr.number}${pr.state ? ` ${pr.state}` : ''}`, href: pr.url })) },
      { title: 'Agents', items: related.agents.map((agent) => ({ text: agentTitle(agent), link: agentDetailLink(agent.id) })) },
    ]
    return links.length > 0 ? [{ title: 'Links', items: links }, ...groups] : groups
  })

  constructor() {
    // A chip of the list names a machine in the fragment: its section opens and scrolls into view, once.
    effect(() => {
      const fragment = this.fragment()
      const sections = this.sections()
      const marker = `${this.repositoryKey()}#${fragment}`
      if (fragment === null || marker === this.revealed) return
      const target = sections.find((section) => section.domId() === fragment)
      if (target === undefined) return
      this.revealed = marker
      target.reveal()
    })
  }

  /** The repository entry of the preferred checkout, with the merged host and name: what the code browser's address is built from. */
  private primaryEntry(summary: MergedRepository) {
    return { ...summary.checkouts[0].repository, host: summary.host, name: summary.slug }
  }

  protected machineOf(machineId: string) {
    return this.machines().find((machine) => machine.machine.id === machineId)?.machine
  }
}
