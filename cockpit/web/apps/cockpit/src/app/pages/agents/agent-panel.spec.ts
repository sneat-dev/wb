import { TestBed } from '@angular/core/testing'
import { RegistryAction } from '@cockpit/fleet-data'
import { ClipboardWriter } from '@cockpit/ui/control'
import { openPage } from '../test-harness'
import { AgentDetailPage } from './agent-detail-page'
import { AgentPanelView } from './agent-panel'
import { agentsDocument } from './agents-fixture'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const open = async (id: string, document = agentsDocument()) => {
  const page = await openPage(`/agents/${id}`, AgentDetailPage, document)
  return { ...page, panel: page.root.querySelector('app-agent-panel') as HTMLElement }
}
const facts = (panel: HTMLElement) => Object.fromEntries([...panel.querySelectorAll('dl.facts > dt')].map((term) => [text(term), text(term.nextElementSibling)]))

describe('AgentPanelView', () => {
  // cockpit-views#ac:agent-detail-links-its-work
  it('shows identity, state, a link to its machine, the repository, the worktrees and their branches, the task and the pull requests of those', async () => {
    const { panel } = await open('run-1')
    expect(text(panel.querySelector('h2'))).toBe('claude · sonnet-5-5')
    expect(text(panel.querySelector('.state'))).toContain('running')
    expect(text(panel.querySelector('.state'))).toContain('working')
    expect(text(panel.querySelector('.why'))).toBe('Running for 1 h on alpha')
    expect(panel.querySelector('.blocked')).toBeNull()
    const identity = facts(panel)
    expect(identity['Kind']).toBe('run')
    expect(identity['Runtime']).toBe('claude')
    expect(identity['Model']).toBe('sonnet-5-5')
    expect(identity['Run id']).toBe('run-1')
    expect(identity['Task']).toBe('fix-ci')
    expect(identity['Repository']).toBe('acme/r1')
    expect(panel.querySelector('dd a[href="/machines/mach-alpha"]')?.textContent).toBe('alpha')
    expect(panel.querySelector('dd a[href="/tasks?sel=fix-ci"]')).not.toBeNull()
    expect(panel.querySelector('dd a[href="/repositories/github.com/acme/r1"]')).not.toBeNull()
    const worktrees = panel.querySelectorAll('[aria-label="Worktrees"] li')
    expect(worktrees).toHaveLength(1)
    expect(text(worktrees[0])).toContain('acme/r1')
    expect(text(worktrees[0].querySelector('.branch'))).toBe('fix-ci')
    expect(text(worktrees[0])).toContain('↑2')
    expect(worktrees[0].querySelector('a')?.getAttribute('href')).toBe('/worktrees?sel=w1')
    expect(panel.querySelectorAll('[aria-label="Pull requests"] app-pr-chip')).toHaveLength(1)
  })

  it('copies the full session id, and the id is in the panel and not in the row', async () => {
    const { panel } = await open('s-block')
    const copy = vi.spyOn(TestBed.inject(ClipboardWriter), 'copy').mockResolvedValue(true)
    expect(facts(panel)['Session id']).toContain('wbs-3da4ea95-0000-4000-8000-000000000001')
    const button = panel.querySelector('dd app-copy-icon button') as HTMLButtonElement
    expect(button.getAttribute('aria-label')).toBe('Copy session id')
    button.click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith('wbs-3da4ea95-0000-4000-8000-000000000001'))
  })

  it('says blocked in words only for a blocked agent, and lists the worktrees of several tasks without choosing one task', async () => {
    const { panel } = await open('s-block')
    expect(text(panel.querySelector('.blocked'))).toBe('Blocked: waiting on you')
    expect([...panel.querySelectorAll('dd.list a')].map(text)).toEqual(['add-search', 'zeta'])
    expect(text(panel.querySelector('[aria-label="Work"] dl.facts > dt'))).toBe('Tasks')
    const worktrees = [...panel.querySelectorAll('[aria-label="Worktrees"] li')].map(text)
    expect(worktrees).toHaveLength(2)
    expect(worktrees[0]).toContain('task add-search')
    expect(worktrees[1]).toContain('task zeta')
  })

  it('says plainly that a session cannot be controlled from here, and offers no command and no action', async () => {
    const { panel } = await open('s-free')
    expect(text(panel.querySelector('.control'))).toBe('This session was not started by wb; it cannot be stopped or messaged from here.')
    expect(panel.querySelectorAll('app-copy-command-list li, app-copy-command-list button')).toHaveLength(0)
    expect(panel.querySelector('app-action-slot')).toBeNull()
    expect(text(panel.querySelector('.state'))).toContain('state not reported')
    expect(text(panel.querySelector('[aria-label="Work"] dl.facts'))).toContain('none: no worktree or task is linked')
    expect(text(panel.querySelector('[aria-label="Worktrees"]'))).toContain('None: no worktree is linked')
    expect(text(panel.querySelector('[aria-label="Pull requests"]'))).toContain('None')
    expect(facts(panel)['Repository']).toBe('not reported')
  })

  it('offers the library status, logs and stop of a dispatched run, and the action slot', async () => {
    const { panel } = await open('run-1')
    const commands = text(panel.querySelector('app-copy-command-list'))
    expect(commands).toContain("wb agent status 'run-1'")
    expect(commands).toContain("wb agent logs 'run-1'")
    expect(commands).toContain("wb agent stop 'run-1'")
    expect(panel.querySelector('.control')).toBeNull()
    expect(panel.querySelector('app-action-slot')).not.toBeNull()
  })

  it('labels an agent of another machine with that machine and its age, and offers it no command', async () => {
    const { panel } = await open('s-beta')
    expect(facts(panel)['Machine']).toContain('beta')
    expect(facts(panel)['Machine']).toContain('cached, 5 h ago')
    expect(facts(panel)['Runtime']).toBe('codex')
    expect(facts(panel)['Model']).toBe('not reported')
    expect(panel.querySelector('app-action-slot')).toBeNull()
    expect(panel.querySelectorAll('app-copy-command-list li')).toHaveLength(0)
    const document = agentsDocument()
    document.agents[0] = { ...document.agents[0], machine: 'beta', machine_id: 'mach-beta', route: 'cached' }
    const run = await open('run-1', document)
    expect(text(run.panel.querySelector('.control'))).toContain('This run is reported by beta (cached, 5 min ago)')
    expect(run.panel.querySelector('app-copy-command-list li')).toBeNull()
    expect(run.panel.querySelector('app-action-slot')).toBeNull()
  })

  it('says a finished run finished, with its exit code, and a run with no timestamps says so', async () => {
    const failed = await open('run-fail')
    expect(text(failed.panel.querySelector('.why'))).toBe('Finished 3 h ago, exit code 1')
    expect(text(failed.panel.querySelector('.state'))).toContain('failed')
    const bare = await open('run-bare')
    expect(text(bare.panel.querySelector('.why'))).toBe('Finished; no end time reported')
    expect(text(bare.panel.querySelector('h2'))).toBe('agent')
  })

  it('shows the worktree of another machine with its machine chip', async () => {
    const document = agentsDocument()
    document.agents[0] = { ...document.agents[0], worktrees: ['w1', 'w3'] }
    const { panel } = await open('run-1', document)
    expect(text(panel.querySelectorAll('[aria-label="Worktrees"] li')[1])).toContain('beta')
  })

  it('names a repository the document does not list by its id, as a plain link, and renders nothing for an agent that has vanished', async () => {
    const document = agentsDocument()
    document.agents[0] = { ...document.agents[0], repository: 'ghost' }
    const { panel, store, harness } = await open('run-1', document)
    expect(panel.querySelector('dl.facts a[href^="/repositories"]')).not.toBeNull()
    store.document.set({ ...document, agents: [] })
    harness.detectChanges()
    const fixture = TestBed.createComponent(AgentPanelView)
    fixture.componentRef.setInput('id', 'run-1')
    await fixture.whenStable()
    expect(text(fixture.nativeElement)).toBe('')
  })

  it('copies the run id of a run, the entry id of an agent that has neither, and shows a pull request that names no repository', async () => {
    const document = agentsDocument()
    document.agents[1] = { ...document.agents[1], session_id: undefined }
    document.pull_requests = [{ ...document.pull_requests[0], repository: undefined, worktree: 'w1' }]
    const bare = await open('s-block', document)
    expect(facts(bare.panel)['Session id']).toBe('s-block')
    const run = await open('run-1', document)
    expect(facts(run.panel)['Run id']).toBe('run-1')
    expect(text(run.panel.querySelector('[aria-label="Pull requests"] li'))).toMatch(/^#7/)
  })

  it('hands the slot of a local run the actions the registry returned for it', async () => {
    await open('run-1')
    const action = { id: 'agent.stop', title: 'Stop', target_types: ['agent'], applicable: true, parameters: [], capability: 'agent.stop', safety: 'guarded', permitted: true } as unknown as RegistryAction
    const fixture = TestBed.createComponent(AgentPanelView)
    fixture.componentRef.setInput('id', 'run-1')
    fixture.componentRef.setInput('registry', new Map([['agent:run-1', [action]]]))
    await fixture.whenStable()
    expect(text(fixture.nativeElement.querySelector('app-action-slot'))).toContain('Stop')
    const bare = TestBed.createComponent(AgentPanelView)
    bare.componentRef.setInput('id', 'run-1')
    await bare.whenStable()
    expect(text(bare.nativeElement.querySelector('app-action-slot'))).toBe('')
  })
})
