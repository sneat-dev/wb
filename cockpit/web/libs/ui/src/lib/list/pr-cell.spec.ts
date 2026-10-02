import { TestBed } from '@angular/core/testing'
import { pullRequest } from '@cockpit/fleet-data/testing'
import { PrCell } from './pr-cell'

function render(pullRequests: unknown[]) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(PrCell)
  fixture.componentRef.setInput('pullRequests', pullRequests)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('PrCell', () => {
  it('shows the control surface\'s pull request chip, checked against an address that is a web address', async () => {
    const root = await render([pullRequest('p1', 'r1', 'w1', { number: 7 })])
    expect(root.querySelector('app-pr-chip .number')?.textContent).toBe('#7')
    expect(root.querySelector('a.number')?.getAttribute('href')).toBe('https://github.com/acme/r1/pull/1')
    expect(root.querySelector('.more')).toBeNull()
    // Remote data that is not a web address never becomes a link.
    const hostile = await render([pullRequest('p2', 'r1', 'w1', { number: 8, url: 'javascript:alert(1)' })])
    expect(hostile.querySelector('a')).toBeNull()
    expect(hostile.querySelector('.number')?.textContent).toBe('#8')
  })

  it('says how many more there are, and shows nothing for none', async () => {
    const two = await render([pullRequest('p1', 'r1', 'w1'), pullRequest('p2', 'r1', 'w1', { number: 2 })])
    expect(two.querySelector('.more')?.textContent).toBe('+1')
    expect((await render([])).textContent?.trim()).toBe('')
  })
})
