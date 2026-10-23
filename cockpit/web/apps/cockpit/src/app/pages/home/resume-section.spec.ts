import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { ResumeSection } from './resume-section'

async function render(document = fleet()) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [FIXED_CLOCK, provideRouter([])] })
  const fixture = TestBed.createComponent(ResumeSection)
  fixture.componentRef.setInput('model', modelOf(document))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { root, rows: [...root.querySelectorAll<HTMLElement>('.home-row')] }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('ResumeSection', () => {
  it('lists the last five tasks by activity, newest first, each with its state badge and age', async () => {
    const { root, rows } = await render()
    expect(text(root.querySelector('h2'))).toBe('Resume')
    expect(rows.map((row) => text(row.querySelector('.home-task')))).toEqual(['improve-docs', 'fix-ci-race', 'migrate-auth', 'refactor-cache', 'add-telemetry'])
    expect(rows.map((row) => text(row.querySelector('app-state-badge')))).toEqual(
      ['ready to land', 'checks failed', 'blocked', 'at risk', 'not ready'].map((state) => expect.stringContaining(state)),
    )
    expect(text(rows[0].querySelector('.home-reason'))).toBe('2 worktrees')
    expect(text(rows[1].querySelector('.home-reason'))).toBe('1 worktree')
    expect(text(rows[0].querySelector('app-relative-time'))).toBe('20 min ago')
  })

  it('opens the task on Tasks from one link that covers the row, named for the task', async () => {
    const { rows } = await render()
    const link = rows[1].querySelector('a.home-act') as HTMLAnchorElement
    expect(text(link)).toBe('Open')
    expect(link.classList.contains('stretched')).toBe(true)
    expect(link.getAttribute('href')).toBe('/tasks?sel=fix-ci-race')
    expect(link.getAttribute('aria-label')).toBe('Open task fix-ci-race')
  })

  it('shows an idle task as quiet plain text, not a badge', async () => {
    const { rows } = await render(fleet('healthy'))
    expect(rows.length).toBeGreaterThan(0)
    const idle = rows.find((row) => row.querySelector('.home-idle'))
    expect(idle).toBeDefined()
    expect(text(idle?.querySelector('.home-idle'))).toBe('idle')
    expect(idle?.querySelector('app-state-badge')).toBeNull()
  })

  it('says there are no tasks in one calm line', async () => {
    const empty = fleet('healthy')
    empty.worktrees = []
    empty.agents = []
    const { root, rows } = await render(empty)
    expect(rows).toHaveLength(0)
    expect(text(root.querySelector('.home-calm'))).toBe('No tasks yet.')
  })
})
