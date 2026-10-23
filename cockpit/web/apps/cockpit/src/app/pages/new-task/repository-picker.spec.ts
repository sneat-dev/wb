import { TestBed } from '@angular/core/testing'
import { NOW } from '../test-harness'
import { PICKER_ROWS, RepositoryPicker } from './repository-picker'

const NAMES = ['sneat-co/sneat-go', 'sneat-co/bots-go', 'acme/tools', 'acme/web', 'bad name/x', 'Strongo/Extras']

async function render(chosen: string[] = [], names: string[] = NAMES) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(RepositoryPicker)
  fixture.componentRef.setInput('names', names)
  fixture.componentRef.setInput('chosen', chosen)
  fixture.componentRef.setInput('now', NOW)
  const added: string[][] = []
  const removed: string[] = []
  fixture.componentInstance.added.subscribe((value) => added.push(value))
  fixture.componentInstance.removed.subscribe((value) => removed.push(value))
  await fixture.whenStable()
  const root = fixture.nativeElement as HTMLElement
  const input = root.querySelector('input') as HTMLInputElement
  const type = async (text: string) => {
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await fixture.whenStable()
  }
  const press = async (key: string, init: KeyboardEventInit = {}) => {
    input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }))
    await fixture.whenStable()
  }
  const options = () => [...root.querySelectorAll('[role=option]')].map((option) => option.textContent?.trim())
  const text = (selector: string) => (root.querySelector(selector)?.textContent ?? '').replace(/\s+/g, ' ').trim()
  return { fixture, root, input, type, press, options, added, removed, text }
}

describe('RepositoryPicker', () => {
  it('lists nothing until it is focused or typed in, and then offers only names that can be picked', async () => {
    const { root, input, fixture, options } = await render()
    expect(root.querySelector('[role=listbox]')).toBeNull()
    input.dispatchEvent(new Event('focus'))
    await fixture.whenStable()
    expect(options()).toEqual(['sneat-co/sneat-go', 'sneat-co/bots-go', 'acme/tools', 'acme/web', 'Strongo/Extras'])
    expect(input.getAttribute('aria-expanded')).toBe('true')
    input.dispatchEvent(new Event('blur'))
    await fixture.whenStable()
    expect(root.querySelector('[role=listbox]')).toBeNull()
    expect(input.getAttribute('aria-expanded')).toBe('false')
  })

  it('narrows the list with the wildcard matcher, and a name already chosen is not offered again', async () => {
    const { type, options, root } = await render(['sneat-co/bots-go'])
    await type('sneat-*/*-go')
    expect(options()).toEqual(['sneat-co/sneat-go'])
    await type('ACME')
    expect(options()).toEqual(['acme/tools', 'acme/web'])
    await type('-tools')
    expect(options()).toEqual(['sneat-co/sneat-go', 'acme/web', 'Strongo/Extras'])
    await type('zzz')
    expect(root.querySelector('[role=listbox]')).toBeNull()
    expect(root.querySelector('[role=status]')?.textContent).toBe('No repository matches.')
  })

  it('moves through the list with the arrow keys, wrapping, and chooses the active one with Enter', async () => {
    const { type, press, input, options, added, root } = await render()
    await type('acme')
    expect(input.getAttribute('aria-activedescendant')).toMatch(/-option-0$/)
    await press('ArrowDown')
    expect(root.querySelector('.option.active')?.textContent?.trim()).toBe('acme/web')
    await press('ArrowDown')
    expect(root.querySelector('.option.active')?.textContent?.trim()).toBe('acme/tools')
    await press('ArrowUp')
    expect(root.querySelector('.option.active')?.textContent?.trim()).toBe('acme/web')
    await press('Enter')
    expect(added).toEqual([['acme/web']])
    expect(options()).toHaveLength(2)
  })

  it('chooses with a click, and keeps the focus in the input by not taking the mouse down', async () => {
    const { type, root, added } = await render()
    await type('web')
    const option = root.querySelector('[role=option]') as HTMLElement
    const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    option.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    option.click()
    expect(added).toEqual([['acme/web']])
  })

  it('adds every match of a pattern at once, from the button or with Ctrl+Enter, and clears the text', async () => {
    const { type, root, added, input, press, fixture } = await render()
    await type('sneat-co/sneat-go')
    expect(root.querySelector('.add-all')).toBeNull()
    await type('sneat-*/*-go')
    expect((root.querySelector('.add-all') as HTMLElement).textContent?.trim()).toBe('Add all 2 matches')
    ;(root.querySelector('.add-all') as HTMLElement).click()
    await fixture.whenStable()
    expect(added).toEqual([['sneat-co/sneat-go', 'sneat-co/bots-go']])
    expect(input.value).toBe('')
    await type('acme/')
    await press('Enter', { ctrlKey: true })
    await type('web')
    await press('Enter', { metaKey: true })
    expect(added).toEqual([['sneat-co/sneat-go', 'sneat-co/bots-go'], ['acme/tools', 'acme/web'], ['acme/web']])
  })

  it('does nothing for Enter or Add all when nothing matches, and ignores the other keys', async () => {
    const { type, press, added, fixture } = await render()
    await type('zzz')
    await press('Enter')
    await press('Enter', { ctrlKey: true })
    await press('ArrowDown')
    await press('a')
    expect(added).toEqual([])
    expect(fixture.componentInstance).toBeTruthy()
  })

  it('clears the text with Escape, and lets Escape through when there is none to clear', async () => {
    const { type, press, input, fixture } = await render()
    await type('acme')
    const first = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    const stop = vi.spyOn(first, 'stopPropagation')
    input.dispatchEvent(first)
    await fixture.whenStable()
    expect(input.value).toBe('')
    expect(stop).toHaveBeenCalled()
    const second = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    const pass = vi.spyOn(second, 'stopPropagation')
    input.dispatchEvent(second)
    expect(pass).not.toHaveBeenCalled()
    await press('x')
  })

  it('shows at most eight matches and says how many more there are', async () => {
    const many = Array.from({ length: PICKER_ROWS + 3 }, (_, index) => `org/repo-${index}`)
    const { type, options, text } = await render([], many)
    await type('repo')
    expect(options()).toHaveLength(PICKER_ROWS)
    expect(text('.more')).toBe('3 more: narrow the filter')
  })

  it('shows what is chosen as chips with a remove button each, and says so when nothing is', async () => {
    const empty = await render()
    expect(empty.text('.chosen')).toBe('None chosen yet')
    const { root, removed } = await render(['acme/web', 'acme/tools'])
    expect([...root.querySelectorAll('.chip-name')].map((chip) => chip.textContent)).toEqual(['acme/web', 'acme/tools'])
    ;(root.querySelector('button[aria-label="Remove acme/tools"]') as HTMLElement).click()
    expect(removed).toEqual(['acme/tools'])
  })

  it('labels the input and gives it the combobox role', async () => {
    const { root, input } = await render()
    expect(root.querySelector('label')?.getAttribute('for')).toBe(input.id)
    expect(input.getAttribute('role')).toBe('combobox')
  })

  it('keeps the active row in range as the matches shrink after a choice', async () => {
    const { type, press, added, root, fixture } = await render()
    await type('acme')
    await press('ArrowDown')
    ;(root.querySelectorAll('[role=option]')[1] as HTMLElement).click()
    fixture.componentRef.setInput('chosen', ['acme/web'])
    await fixture.whenStable()
    expect(added).toEqual([['acme/web']])
    expect(root.querySelector('.option.active')?.textContent?.trim()).toBe('acme/tools')
  })
})
