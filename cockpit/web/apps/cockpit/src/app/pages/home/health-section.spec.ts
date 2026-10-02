import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { buildHealth } from '@cockpit/fleet-data/home-details'
import { ClipboardWriter } from '@cockpit/ui/control'
import { fleet, modelOf } from './home-testing'
import { HealthSection } from './health-section'
import { healthRows } from './health-rows'

async function render(rows = healthRows(buildHealth(modelOf(fleet())), 0)) {
  const copy = vi.fn().mockResolvedValue(true)
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: ClipboardWriter, useValue: { copy } }] })
  const fixture = TestBed.createComponent(HealthSection)
  fixture.componentRef.setInput('rows', rows)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, copy, items: [...root.querySelectorAll<HTMLElement>('.home-row')] }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('HealthSection', () => {
  it('has one line per problem, each with where its command runs and "Copy"', async () => {
    const { root, items, fixture, copy } = await render()
    expect(text(root.querySelector('h2'))).toBe('Fleet health 2')
    expect(root.querySelector('.home-count')?.classList.contains('warn')).toBe(true)
    expect(items.map((item) => text(item.querySelector('.home-task')))).toEqual(['old has not published for over 24 hours', 'old runs an older WB (0.170.2)'])
    expect(items.map((item) => text(item.querySelector('.home-where-run')))).toEqual(['run on old', 'run on old'])
    expect(items.map((item) => text(item.querySelector('button')))).toEqual(['Copy', 'Copy'])
    expect(items.map((item) => item.querySelector('button')?.getAttribute('aria-label'))).toEqual([
      'Copy wb remote publish: old has not published for over 24 hours',
      'Copy wb self-update: old runs an older WB (0.170.2)',
    ])
    expect(items[0].querySelector('a.home-task')?.getAttribute('href')).toBe('/machines?chips=stale')
    expect(items[1].querySelector('a.home-task')?.getAttribute('href')).toBe('/machines?chips=outdated')
    ;(items[0].querySelector('button') as HTMLButtonElement).click()
    await fixture.whenStable()
    expect(copy).toHaveBeenCalledWith('wb remote publish')
  })

  it('calls a command with a part to edit a template, says why there is none when the library has none, and leaves a line with no link or command plain', async () => {
    const { items } = await render([
      { id: 'a', text: 'remote needs enrolling', link: undefined, where: 'run here', command: { text: 'wb remote enroll --hub-url=<<<edit:hub-url>>>', needsEdit: true } },
      { id: 'b', text: 'vm is still scanning', link: undefined, where: undefined, command: { reason: 'nothing to run: it clears when the machine finishes its first scan' } },
      { id: 'c', text: '2 entries were left out', link: undefined, where: undefined, command: undefined },
    ])
    expect(text(items[0].querySelector('button'))).toBe('Copy template')
    expect(items[0].querySelector('a')).toBeNull()
    expect(text(items[0].querySelector('.home-where-run'))).toBe('run here')
    expect(text(items[1].querySelector('.home-reason'))).toBe('nothing to run: it clears when the machine finishes its first scan')
    expect(items[1].querySelector('button')).toBeNull()
    expect(items[2].querySelector('button, .home-reason, .home-where-run')).toBeNull()
  })
})
