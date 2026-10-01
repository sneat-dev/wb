import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { buildCleanup } from '@cockpit/fleet-data/home-details'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { ClipboardWriter } from '@cockpit/ui/control'
import { CleanupSection } from './cleanup-section'
import { fleet, modelOf } from './home-testing'

async function render(document = fleet()) {
  const chart = { update: vi.fn(), destroy: vi.fn() }
  const create = vi.fn(() => chart)
  const copy = vi.fn().mockResolvedValue(true)
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [provideRouter([]), { provide: CHART_ENGINE, useValue: async () => ({ create }) }, { provide: ClipboardWriter, useValue: { copy } }],
  })
  const fixture = TestBed.createComponent(CleanupSection)
  fixture.componentRef.setInput('cleanup', buildCleanup(modelOf(document)))
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement, create, copy }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('CleanupSection', () => {
  it('is one line: how many are safe to remove and how many need a look, each a link, and says the counts are indicative', async () => {
    const { root } = await render()
    expect(text(root.querySelector('h2'))).toBe('Cleanup')
    expect(text(root.querySelector('.cleanup-text'))).toBe('9 safe to remove; 9 need a look (indicative: the cleanup operation decides)')
    const [safe, look] = [...root.querySelectorAll<HTMLAnchorElement>('.cleanup-text a')]
    expect(safe.getAttribute('href')).toBe('/worktrees?chips=safe')
    expect(look.getAttribute('href')).toBe('/worktrees?chips=look')
  })

  it('has "Review & clean" open Worktrees filtered to the safe set, and "Copy template" for the dry run, which is never applied', async () => {
    const { fixture, root, copy } = await render()
    const review = root.querySelector('a.home-act') as HTMLAnchorElement
    expect(text(review)).toBe('Review & clean')
    expect(review.getAttribute('href')).toBe('/worktrees?chips=safe')
    const button = root.querySelector('app-lazy-copy button') as HTMLButtonElement
    expect(text(button)).toBe('Copy template')
    button.click()
    await fixture.whenStable()
    await new Promise((done) => setTimeout(done, 10))
    expect(copy).toHaveBeenCalledWith('wb worktree cleanup <<<edit:task>>>')
  })

  it('expands to the worktree age bars, each linking to Worktrees with its age term, and creates the chart only then', async () => {
    const { fixture, root, create } = await render()
    const toggle = root.querySelector('button.home-toggle') as HTMLButtonElement
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(root.querySelector('app-chart')).toBeNull()
    toggle.click()
    await fixture.whenStable()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(root.querySelector('#home-cleanup-chart app-chart')).not.toBeNull()
    await vi.waitFor(() => expect(create).toHaveBeenCalled())
    const bars = root.querySelectorAll('.data tbody tr')
    expect([...bars].map((row) => text(row))).toEqual(['today', '1 to 7 days', '8 to 30 days', '31 to 90 days', 'over 90 days'].map((label) => expect.stringContaining(label)))
    expect(root.querySelectorAll('.data tbody button')).toHaveLength(5)
    toggle.click()
    await fixture.whenStable()
    expect(root.querySelector('app-chart')).toBeNull()
  })

  it('opens Worktrees with the age term of a bar that is chosen', async () => {
    const { fixture, root } = await render()
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true)
    ;(root.querySelector('button.home-toggle') as HTMLButtonElement).click()
    await fixture.whenStable()
    const first = root.querySelector('.data tbody button') as HTMLButtonElement
    first.click()
    expect(navigate).toHaveBeenCalledWith(['/worktrees'], { queryParams: { q: 'age:<1d' } })
  })

  it('says how many worktrees have no reported activity time and are in no bar', async () => {
    const document = fleet()
    document.worktrees[0].last_activity_at = undefined
    const { fixture, root } = await render(document)
    ;(root.querySelector('button.home-toggle') as HTMLButtonElement).click()
    await fixture.whenStable()
    expect(text(root.querySelector('.home-note'))).toBe('1 with no reported activity time are in no bar.')
    const clean = await render(fleet())
    ;(clean.root.querySelector('button.home-toggle') as HTMLButtonElement).click()
    await clean.fixture.whenStable()
    expect(clean.root.querySelector('.home-note')).toBeNull()
  })

  it('collapses to one calm line when there is nothing to clean up', async () => {
    const { root } = await render(fleet('healthy'))
    expect(text(root.querySelector('.home-calm'))).toBe('Nothing to clean up.')
    expect(root.querySelector('.cleanup-line')).toBeNull()
  })
})
