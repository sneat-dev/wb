import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from '@cockpit/ui/control'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { GalleryPage } from './gallery-page'

async function render() {
  const create = vi.fn(() => ({ update: vi.fn(), destroy: vi.fn() }))
  TestBed.configureTestingModule({ providers: [{ provide: CHART_ENGINE, useValue: async () => ({ create }) }, { provide: ClipboardWriter, useValue: { copy: async () => true } }] })
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
    expect(root.querySelectorAll('app-action-slot .slot')).toHaveLength(2)
    expect(root.querySelector('app-owner-signin')).not.toBeNull()
    expect(root.querySelectorAll('app-chart')).toHaveLength(6)
    await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(6))
  })

  it('shows an absent registry as nothing at all', async () => {
    const { root } = await render()
    const row = [...root.querySelectorAll('.slot-row')][2]
    expect(row.querySelector('.slot')).toBeNull()
    expect(row.children).toHaveLength(2)
    expect(row.textContent).toBe('worktree wt-1')
  })

  it('says what an interaction did and that nothing ran', async () => {
    const { fixture, root } = await render()
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
