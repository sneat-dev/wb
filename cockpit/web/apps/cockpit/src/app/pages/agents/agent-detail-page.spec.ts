import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { openPage } from '../test-harness'
import { agentsDocument } from './agents-fixture'
import { AgentDetailPage } from './agent-detail-page'
import { AgentsPage } from './agents-page'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const panelOf = (root: HTMLElement) => root.querySelector('app-agent-panel') as HTMLElement

describe('AgentDetailPage', () => {
  // cockpit-views#ac:detail-routes-render-the-same-panel
  it("is the side panel's content, from the same component, ending with the collapsed Raw data block", async () => {
    const page = await openPage('/agents/run-1', AgentDetailPage, agentsDocument())
    const list = await openPage('/agents?sel=run-1', AgentsPage, agentsDocument())
    for (const selector of ['.state', 'dl.facts', '[aria-label="Work"]', '[aria-label="Worktrees"]', '[aria-label="Pull requests"]', '[aria-label="Copy command"]']) {
      expect(text(panelOf(page.root).querySelector(selector)), selector).toBe(text(panelOf(list.root).querySelector(selector)))
    }
    expect(panelOf(page.root).querySelector('.content')?.classList.contains('page')).toBe(true)
    expect(panelOf(list.root).querySelector('.content')?.classList.contains('page')).toBe(false)
    expect(page.root.querySelector('.back a')?.getAttribute('href')).toBe('/agents')
    const sections = panelOf(page.root).querySelectorAll('section')
    expect(sections[sections.length - 1].getAttribute('aria-label')).toBe('Raw data')
    expect((panelOf(page.root).querySelector('details') as HTMLDetailsElement).open).toBe(false)
  })

  it('says it is waiting for the snapshot, and that the agent is not there once the snapshot is complete', async () => {
    const warming = await openPage('/agents/x', AgentDetailPage, fleetDocument({ warming_up: true, agents: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openPage('/agents/nope', AgentDetailPage, agentsDocument())
    expect(text(gone.root)).toContain('This agent is not in the fleet document')
    expect(gone.root.querySelector('app-agent-panel')).toBeNull()
    const unread = await openPage('/agents/run-1', AgentDetailPage, agentsDocument())
    unread.store.loaded.set(false)
    unread.store.document.set(fleetDocument({ agents: [] }))
    unread.harness.detectChanges()
    expect(text(unread.root)).toContain('Waiting for the fleet snapshot')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(AgentDetailPage)
    fixture.componentRef.setInput('id', 'x')
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
