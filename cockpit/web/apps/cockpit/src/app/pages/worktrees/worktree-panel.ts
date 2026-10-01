import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { FleetStore, PanelCommand, RegistryAction, TaskView, Worktree, agentDetailLink, agentTitle, isOpenPullRequest, repositoryDetailLink, routeLabel, taskDetailLink, webAddress } from '@cockpit/fleet-data'
import { branchCleanup, branchList } from '@cockpit/fleet-data/commands'
import { PullRequestPanel, WorktreePanel, buildPullRequestPanel, buildWorktreePanel } from '@cockpit/fleet-data/panel'
import { ActionSlot } from '@cockpit/ui/control'
import { CodeIndexPanel } from '@cockpit/ui/code-index-panel'
import { PanelContent, PanelFact, PanelRelated } from '@cockpit/ui/panel'

/**
 * One worktree's content: the side panel of the Worktrees list and the page of
 * `/worktrees/:id` are both this component (REQ:detail-routes-share-the-panel).
 * It is built from `buildWorktreePanel(model, id)`; the standard blocks
 * (facts, related entities, commands, raw data) are the panel component's.
 */
@Component({
  selector: 'app-worktree-panel',
  imports: [PanelContent, CodeIndexPanel, ActionSlot],
  templateUrl: './worktree-panel.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreePanelView {
  readonly id = input.required<string>()
  /** The detail page, not the side panel. */
  readonly page = input(false)
  /**
   * What the action registry returned, by target (`worktree:<id>`, `pull_request:<id>`). The page that has the
   * cockpit-actions client passes it; without it, or without an entry for a target, no action area is drawn.
   */
  readonly registry = input<ReadonlyMap<string, readonly RegistryAction[]>>()

  protected readonly store = inject(FleetStore)
  /** The worktree's entry and its panel data; none for an id the document does not list. */
  protected readonly data = computed(() => {
    const model = this.store.model()
    const entry = model.worktreeById(this.id())
    return entry === undefined ? undefined : { entry, view: buildWorktreePanel(model, entry.id) as WorktreePanel }
  })

  /** The action slots of this worktree and of its pull requests, only for entries of this machine and only where the registry offered something. */
  protected readonly slots = computed(() => {
    const { entry } = this.data() as { entry: Worktree }
    const pullRequests = this.store.model().worktreePullRequests.get(entry.id) ?? []
    const targets = [...(entry.route === 'local' ? [`worktree:${entry.id}`] : []), ...pullRequests.filter((pr) => pr.route === 'local').map((pr) => `pull_request:${pr.id}`)]
    return targets.flatMap((target) => {
      const actions = this.registry()?.get(target)
      return actions && actions.length > 0 ? [{ target, actions }] : []
    })
  })

  /** The library's worktree commands, this branch's `wb branch` commands (a dry-run plan for cleanup) and `wb pr land` for each open pull request, each labelled where it runs. */
  protected readonly commands = computed<PanelCommand[]>(() => {
    const { entry, view } = this.data() as { entry: Worktree; view: WorktreePanel }
    const model = this.store.model()
    const target = model.targetOf(entry)
    const land = (model.worktreePullRequests.get(entry.id) ?? []).filter(isOpenPullRequest).flatMap((pr) => {
      const panel = buildPullRequestPanel(model, pr.id) as PullRequestPanel
      // A pull request with no repository has no command to copy.
      return panel.commands.map((command) => ({ ...command, title: `${command.title} ${panel.summary.repository as string}#${pr.number}` }))
    })
    return [
      ...view.commands,
      { title: 'List this branch', command: branchList(view.summary.repository, view.summary.branch, target) },
      { title: 'Plan branch cleanup (dry run)', command: branchCleanup(view.summary.repository, view.summary.branch, target) },
      ...land,
    ]
  })

  protected readonly facts = computed<PanelFact[]>(() => {
    const { entry, view } = this.data() as { entry: Worktree; view: WorktreePanel }
    const { summary, related } = view
    const sync = [summary.ahead ? `${summary.ahead} ahead` : '', summary.behind ? `${summary.behind} behind` : '', summary.upstreamGone ? 'upstream gone' : ''].filter((part) => part !== '')
    const facts: PanelFact[] = [
      { label: 'Task', text: summary.task, link: taskDetailLink(summary.task), copy: true },
      related.repository === undefined
        ? { label: 'Repository', text: summary.repository }
        : { label: 'Repository', text: summary.repository, link: repositoryDetailLink(related.repository.host, related.repository.slug) },
      { label: 'Branch', text: summary.branch, copy: true },
      { label: 'Machine', text: summary.machine },
      { label: 'Source', text: routeLabel(entry, this.store.now()) },
      { label: 'State', text: `${summary.ownerState ?? 'unknown'}${summary.lifecycle ? `, ${summary.lifecycle}` : ''}` },
    ]
    if (entry.route === 'local') facts.push({ label: 'Sync (this machine)', text: sync.length > 0 ? sync.join(', ') : 'in sync' })
    facts.push({ label: 'Last activity', time: summary.lastActivityAt, text: '—' })
    return facts
  })

  protected readonly related = computed<PanelRelated[]>(() => {
    const { agents, task } = (this.data() as { view: WorktreePanel }).view.related
    const pullRequests = this.store.model().worktreePullRequests.get(this.id()) ?? []
    return [
      { title: 'Task', items: [{ text: `${(task as TaskView).name} (${(task as TaskView).stateInfo.label})`, link: taskDetailLink((task as TaskView).name) }] },
      { title: 'Pull requests', items: pullRequests.map((pr) => ({ text: `#${pr.number}${pr.state ? ` ${pr.state}` : ''}`, href: webAddress(pr.url) })) },
      { title: 'Agents', items: agents.map((agent) => ({ text: agentTitle(agent), link: agentDetailLink(agent.id) })) },
    ]
  })
}
