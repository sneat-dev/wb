import { TestBed } from '@angular/core/testing'
import { PullRequest } from '@cockpit/fleet-data'
import { pullRequest } from '@cockpit/fleet-data/testing'
import { PULL_REQUESTS_SHOWN, TaskPrCell } from './task-pr-cell'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

async function render(pullRequests: PullRequest[]): Promise<HTMLElement> {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(TaskPrCell)
  fixture.componentRef.setInput('pullRequests', pullRequests)
  await fixture.whenStable()
  return fixture.nativeElement
}

const pr = (id: string, extra: Partial<PullRequest> = {}) => pullRequest(id, 'r1', 'w1', { number: Number(id.slice(1)), ...extra })

describe('TaskPrCell', () => {
  it('shows nothing for a task with no pull request', async () => {
    expect(text(await render([]))).toBe('')
  })

  it('shows the number as a link for a web address and the checks passed over total', async () => {
    const root = await render([pr('p7', { checks_total: 4, checks_passed: 3, checks_green: false, checks_pending: 1 })])
    expect(root.querySelector('a.number')?.getAttribute('href')).toBe('https://github.com/acme/r1/pull/1')
    expect(root.querySelector('a.number')?.getAttribute('tabindex')).toBe('-1')
    expect(text(root)).toContain('#7')
    expect(text(root)).toContain('3/4')
  })

  it('shows no link for an address that is not a web address', async () => {
    const root = await render([pr('p1', { url: 'javascript:alert(1)' })])
    expect(root.querySelector('a')).toBeNull()
    expect(text(root.querySelector('.number'))).toBe('#1')
  })

  it('shows open pull requests before the others, at most two, and +n for the rest', async () => {
    const root = await render([pr('p1', { state: 'merged' }), pr('p2', { state: 'draft' }), pr('p3'), pr('p4', { state: 'closed' })])
    expect([...root.querySelectorAll('.number')].map(text)).toEqual(['#2', '#3'])
    expect(text(root.querySelector('.more'))).toBe('+2')
    expect(PULL_REQUESTS_SHOWN).toBe(2)
  })

  it('shows the state of a pull request that merged or closed, and "not checked" for one never observed', async () => {
    const root = await render([pr('p1', { state: 'merged' }), pr('p2', { checked_at: undefined, state: undefined })])
    expect(text(root)).toContain('merged')
    expect(text(root.querySelector('.unobserved'))).toBe('not checked')
    expect(root.querySelector('.unobserved')?.getAttribute('title')).toBe('Not yet checked')
    expect(root.querySelector('.more')).toBeNull()
  })
})
