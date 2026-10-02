import { TestBed } from '@angular/core/testing'
import { ActivatedRoute, convertToParamMap } from '@angular/router'
import { ClipboardWriter } from '@cockpit/ui/control'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { GalleryPage } from './gallery-page'

async function render(query: Record<string, string> = {}) {
  const create = vi.fn(() => ({ update: vi.fn(), destroy: vi.fn() }))
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [
      { provide: CHART_ENGINE, useValue: async () => ({ create }) },
      { provide: ClipboardWriter, useValue: { copy: async () => true } },
      { provide: ActivatedRoute, useValue: { snapshot: { queryParamMap: convertToParamMap(query) } } },
    ],
  })
  const fixture = TestBed.createComponent(GalleryPage)
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement, create }
}

describe('GalleryPage', () => {
  it('shows every component in every state: badges, sync badges, chips, command lists, slots, sign-in and charts', async () => {
    const { root, create } = await render()
    expect(root.querySelectorAll('app-state-badge').length).toBeGreaterThan(50)
    expect(root.querySelectorAll('app-pr-chip')).toHaveLength(6)
    expect(root.querySelectorAll('app-machine-chip')).toHaveLength(5)
    expect(root.querySelectorAll('app-copy-command-list li').length).toBeGreaterThan(15)
    // No page handles an action: the slots are the Copy control of their command, and nothing is a live button.
    expect(root.querySelectorAll('app-action-slot .slot')).toHaveLength(0)
    expect(root.querySelectorAll('app-action-slot .action')).toHaveLength(0)
    expect([...root.querySelectorAll('app-action-slot button')].map((button) => button.textContent?.trim())).toEqual(['Copy template', 'Copy template'])
    expect(root.querySelector('app-owner-signin')).not.toBeNull()
    expect(root.querySelectorAll('app-chart')).toHaveLength(6)
    await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(6))
  })

  it('copies the command of a slot that has no handler, built when its button is pressed', async () => {
    const copy = vi.fn().mockResolvedValue(true)
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({
      providers: [
        { provide: CHART_ENGINE, useValue: async () => ({ create: () => ({ update: vi.fn(), destroy: vi.fn() }) }) },
        { provide: ClipboardWriter, useValue: { copy } },
        { provide: ActivatedRoute, useValue: { snapshot: { queryParamMap: convertToParamMap({}) } } },
      ],
    })
    const fixture = TestBed.createComponent(GalleryPage)
    await fixture.whenStable()
    ;(fixture.nativeElement.querySelector('app-action-slot button') as HTMLButtonElement).click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith("wb pr create 'fix-ci' --commit-all --message=<<<edit:message>>>"))
  })

  it('shows an absent registry as nothing at all', async () => {
    const { root } = await render()
    const row = [...root.querySelectorAll('.slot-row')][2]
    expect(row.querySelector('.slot')).toBeNull()
    expect(row.children).toHaveLength(2)
    expect(row.textContent).toBe('worktree wt-1')
  })

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  it('binds a handler only when the address asks for it, and then draws the registry\'s buttons', async () => {
    const { root } = await render({ handler: '1' })
    expect(root.querySelectorAll('app-action-slot .slot')).toHaveLength(2)
    expect(root.querySelectorAll('app-action-slot app-lazy-copy')).toHaveLength(0)
  })

  it('says what an interaction did and that nothing ran', async () => {
    const { fixture, root } = await render({ handler: '1' })
    const notes = () => [...root.querySelectorAll('.note')].map((note) => note.textContent)
    ;(root.querySelector('app-action-slot .action') as HTMLButtonElement).click()
    ;(root.querySelector('app-copy-command-list app-copy-button button') as HTMLButtonElement).click()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(notes().join(' ')).toMatch(/Copied: wb /)
    })
    expect(notes().join(' ')).toContain('Emitted pr.create for worktree:wt-1; nothing ran.')
    ;(root.querySelector('app-chart .data button') as HTMLButtonElement).click()
    fixture.detectChanges()
    expect(notes().join(' ')).toContain('Emitted the link /worktrees?q=age%3A%3C1d')
  })
})
