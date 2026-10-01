import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { FleetStore, TaskView, Worktree, WorktreePanel, agentDetailLink, agentTitle, repositoryDetailLink, routeLabel, taskDetailLink } from '@cockpit/fleet-data'
import { CodeIndexPanel } from '@cockpit/ui/code-index-panel'
import { PanelContent, PanelFact, PanelRelated } from '@cockpit/ui/panel'

/** A pull request's address when it is a secure web address, else none (the page links nothing it has not checked). */
function webAddress(url: string | undefined): string | undefined {
  return url?.startsWith('https://') ? url : undefined
}

/**
 * One worktree's content: the side panel of the Worktrees list and the page of
 * `/worktrees/:id` are both this component (REQ:detail-routes-share-the-panel).
 * It is built from the view model's `worktreeView(id)`; the standard blocks
 * (facts, related entities, commands, raw data) are the panel component's.
 */
@Component({
  selector: 'app-worktree-panel',
  imports: [PanelContent, CodeIndexPanel],
  templateUrl: './worktree-panel.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreePanelView {
  readonly id = input.required<string>()
  /** The detail page, not the side panel. */
  readonly page = input(false)

  protected readonly store = inject(FleetStore)
  /** The worktree's entry and its panel data; none for an id the document does not list. */
  protected readonly data = computed(() => {
    const model = this.store.model()
    const entry = model.worktreeById(this.id())
    return entry === undefined ? undefined : { entry, view: model.worktreeView(entry.id) as WorktreePanel }
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
    const { pullRequests, agents, task } = (this.data() as { view: WorktreePanel }).view.related
    return [
      { title: 'Task', items: [{ text: `${(task as TaskView).name} (${(task as TaskView).stateInfo.label})`, link: taskDetailLink((task as TaskView).name) }] },
      { title: 'Pull requests', items: pullRequests.map((pr) => ({ text: `#${pr.number}${pr.state ? ` ${pr.state}` : ''}`, href: webAddress(pr.url) })) },
      { title: 'Agents', items: agents.map((agent) => ({ text: agentTitle(agent), link: agentDetailLink(agent.id) })) },
    ]
  })
}
