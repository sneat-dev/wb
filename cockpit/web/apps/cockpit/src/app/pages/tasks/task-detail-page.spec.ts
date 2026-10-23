import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { openPage } from '../test-harness'
import { TaskDetailPage } from './task-detail-page'
import { tasksDocument } from './tasks-fixture'
import { TasksPage } from './tasks-page'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const panelOf = (root: HTMLElement) => root.querySelector('app-task-panel') as HTMLElement

describe('TaskDetailPage', () => {
  // cockpit-views#ac:detail-routes-render-the-same-panel
  it('is the side panel\'s content, from the same component, ending with the collapsed Raw data block', async () => {
    const page = await openPage('/tasks/detail?task=fix-ci', TaskDetailPage, tasksDocument())
    const list = await openPage('/tasks?sel=fix-ci', TasksPage, tasksDocument())
    for (const selector of ['.state', 'dl.facts', '[aria-label="Pull requests"]', '[aria-label="Worktrees"]', '[aria-label="Agents"]', '[aria-label="Copy command"]']) {
      expect(text(panelOf(page.root).querySelector(selector)), selector).toBe(text(panelOf(list.root).querySelector(selector)))
    }
    expect(panelOf(page.root).querySelector('.content')?.classList.contains('page')).toBe(true)
    expect(panelOf(list.root).querySelector('.content')?.classList.contains('page')).toBe(false)
    expect(text(panelOf(page.root).querySelector('h2'))).toBe('fix-ci')
    expect(page.root.querySelector('.back a')?.getAttribute('href')).toBe('/tasks')
    expect(panelOf(page.root).querySelectorAll('section')[panelOf(page.root).querySelectorAll('section').length - 1].getAttribute('aria-label')).toBe('Raw data')
    expect((panelOf(page.root).querySelector('details') as HTMLDetailsElement).open).toBe(false)
  })

  // cockpit-views#ac:task-detail-shows-its-entities
  it('opens for a task name that needs encoding and for an asset-like one, with the summary header, worktrees, branches, pull requests and agents', async () => {
    const encoded = await openPage('/tasks/detail?task=fix%2Fci%20100%25', TaskDetailPage, tasksDocument())
    expect(text(panelOf(encoded.root).querySelector('h2'))).toBe('fix/ci 100%')
    expect(panelOf(encoded.root).querySelector('.state app-state-badge')).not.toBeNull()
    expect(text(panelOf(encoded.root).querySelector('[aria-label="Worktrees"] .branch'))).toBe('fix/ci-100')
    const asset = await openPage('/tasks/detail?task=release.js', TaskDetailPage, tasksDocument())
    expect(text(panelOf(asset.root).querySelector('h2'))).toBe('release.js')
    const full = await openPage('/tasks/detail?task=fix-ci', TaskDetailPage, tasksDocument())
    const panel = panelOf(full.root)
    expect(panel.querySelectorAll('[aria-label="Worktrees"] li')).toHaveLength(3)
    expect(panel.querySelectorAll('[aria-label="Pull requests"] app-pr-chip')).toHaveLength(1)
    expect(panel.querySelectorAll('[aria-label="Agents"] li')).toHaveLength(1)
  })

  it('says it is waiting for the snapshot, that no task was named, and that the task is not there once the snapshot is complete', async () => {
    const warming = await openPage('/tasks/detail?task=x', TaskDetailPage, fleetDocument({ warming_up: true, worktrees: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const unnamed = await openPage('/tasks/detail', TaskDetailPage, tasksDocument())
    expect(text(unnamed.root)).toContain('No task was named')
    const gone = await openPage('/tasks/detail?task=nope', TaskDetailPage, tasksDocument())
    expect(text(gone.root)).toContain('This task is not in the fleet document')
    expect(gone.root.querySelector('app-task-panel')).toBeNull()
    const unread = await openPage('/tasks/detail?task=fix-ci', TaskDetailPage, tasksDocument())
    unread.store.loaded.set(false)
    unread.store.document.set(fleetDocument({ worktrees: [] }))
    unread.harness.detectChanges()
    expect(text(unread.root)).toContain('Waiting for the fleet snapshot')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(TaskDetailPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
