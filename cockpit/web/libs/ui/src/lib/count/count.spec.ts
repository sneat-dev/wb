import { ComponentFixture, TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { CARD_NAME_LIMIT, Count } from './count'

async function render(names: string[]): Promise<ComponentFixture<Count>> {
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const fixture = TestBed.createComponent(Count)
  fixture.componentRef.setInput('label', 'worktrees')
  fixture.componentRef.setInput('names', names)
  fixture.componentRef.setInput('target', '/worktrees')
  fixture.componentRef.setInput('query', { repository: 'r1' })
  await fixture.whenStable()
  return fixture
}

function rect(bottom: number, left: number, top = bottom - 10): DOMRect {
  return { bottom, left, top } as DOMRect
}

describe('Count', () => {
  it('shows the number as a link to the list filtered to its entities', async () => {
    const fixture = await render(['a', 'b'])
    const link: HTMLAnchorElement = fixture.nativeElement.querySelector('a')
    expect(link.textContent).toBe('2')
    expect(link.getAttribute('href')).toBe('/worktrees?repository=r1')
    expect(link.getAttribute('aria-label')).toBe('2 worktrees')
  })

  it('has a hover card that names the entities and holds no control', async () => {
    const fixture = await render(['first', 'second'])
    const root: HTMLElement = fixture.nativeElement
    const card = root.querySelector('.count-card') as HTMLElement
    expect(card.getAttribute('role')).toBe('tooltip')
    expect(root.querySelector('a')?.getAttribute('aria-describedby')).toBe(card.id)
    expect([...card.querySelectorAll('li')].map((li) => li.textContent)).toEqual(['first', 'second'])
    expect(card.textContent).toContain('2 worktrees')
    expect(card.textContent).not.toContain('more')
    expect(card.querySelector('button, a, input, select, [tabindex]')).toBeNull()
    expect(card.classList.contains('open')).toBe(false)
  })

  it('opens on hover and focus, and closes on leave, blur and Escape', async () => {
    const fixture = await render(['a'])
    const root: HTMLElement = fixture.nativeElement
    const card = root.querySelector('.count-card') as HTMLElement
    const open = async (event: string) => {
      root.dispatchEvent(new Event(event))
      await fixture.whenStable()
      expect(card.classList.contains('open')).toBe(true)
    }
    const closed = async (event: Event) => {
      root.dispatchEvent(event)
      await fixture.whenStable()
      expect(card.classList.contains('open')).toBe(false)
    }
    await open('mouseenter')
    await closed(new Event('mouseleave'))
    await open('focusin')
    await closed(new Event('focusout'))
    await open('focusin')
    await closed(new KeyboardEvent('keydown', { key: 'Escape' }))
  })

  it('lists the first names with the total when there are many', async () => {
    const names = Array.from({ length: CARD_NAME_LIMIT + 3 }, (_, index) => `n${index}`)
    const fixture = await render(names)
    const root: HTMLElement = fixture.nativeElement
    expect(root.querySelectorAll('li')).toHaveLength(CARD_NAME_LIMIT)
    expect(root.querySelector('.count-more')?.textContent).toBe('and 3 more')
    expect(root.querySelector('strong')?.textContent).toBe(`${names.length} worktrees`)
  })

  it('names nothing for an empty count and says nothing is missing', async () => {
    const fixture = await render([])
    expect(fixture.nativeElement.querySelector('ul')).toBeNull()
    expect(fixture.nativeElement.querySelector('.count-more')).toBeNull()
  })

  it('places the card below the count, clamped to the screen, or above it near the foot', async () => {
    const fixture = await render(['a'])
    const root: HTMLElement = fixture.nativeElement
    const card = root.querySelector('.count-card') as HTMLElement
    const link = root.querySelector('a') as HTMLElement
    vi.stubGlobal('innerWidth', 375)
    vi.stubGlobal('innerHeight', 800)
    const place = async (box: DOMRect) => {
      vi.spyOn(link, 'getBoundingClientRect').mockReturnValue(box)
      root.dispatchEvent(new Event('mouseenter'))
      await fixture.whenStable()
    }
    await place(rect(100, 20))
    expect([card.style.left, card.style.top, card.style.bottom]).toEqual(['20px', '100px', ''])
    await place(rect(100, 360))
    expect(card.style.left).toBe('79px')
    await place(rect(100, -50))
    expect(card.style.left).toBe('8px')
    await place(rect(700, 20, 690))
    expect([card.style.top, card.style.bottom]).toEqual(['', '110px'])
    vi.unstubAllGlobals()
  })
})
