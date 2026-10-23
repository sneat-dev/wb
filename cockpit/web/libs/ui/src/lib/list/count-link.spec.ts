import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { LinkResult, machineWorktreesLink, taskWorktreesLink } from '@cockpit/fleet-data/list'
import { CountLink } from './count-link'

async function render(inputs: { value?: number; link?: LinkResult; why?: string }) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const fixture = TestBed.createComponent(CountLink)
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  await fixture.whenStable()
  return fixture.nativeElement as HTMLElement
}

describe('CountLink', () => {
  // cockpit-views#ac:every-number-is-a-link
  it('links a count to the list that produced it, with vocabulary terms only', async () => {
    const root = await render({ value: 3, link: taskWorktreesLink('fix ci') })
    const link = root.querySelector('a') as HTMLAnchorElement
    expect(link.textContent).toBe('3')
    expect(decodeURIComponent(link.getAttribute('href') as string)).toBe('/worktrees?q=task:"fix ci"')
    expect((await render({ value: 2, link: machineWorktreesLink('mach-a') })).querySelector('a')?.getAttribute('href')).toBe('/worktrees?machine=mach-a')
  })

  it('shows a number that cannot be a link as plain text with the reason in its title', async () => {
    const root = await render({ value: 4, link: taskWorktreesLink('say "no"') })
    expect(root.querySelector('a')).toBeNull()
    expect(root.querySelector('span')?.textContent).toBe('4')
    expect(root.querySelector('span')?.getAttribute('title')).toContain('double quote')
  })

  it('shows a declared non-linking count as plain text with its reason, and a missing count as a dash', async () => {
    const declared = await render({ value: 7, why: 'there is no branches list page' })
    expect(declared.querySelector('a')).toBeNull()
    expect(declared.querySelector('span')?.getAttribute('title')).toBe('there is no branches list page')
    const bare = await render({ value: 7 })
    expect(bare.querySelector('span')?.hasAttribute('title')).toBe(false)
    expect((await render({})).textContent?.trim()).toBe('—')
  })
})
