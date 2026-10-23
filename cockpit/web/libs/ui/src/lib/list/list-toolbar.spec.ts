import { TestBed } from '@angular/core/testing'
import { ListToolbar } from './list-toolbar'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

async function render(extra: Record<string, unknown> = {}) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(ListToolbar)
  const inputs: Record<string, unknown> = {
    id: 'list-1',
    noun: 'worktrees',
    text: '',
    fields: 'branch, repo',
    shown: 3,
    total: 10,
    chips: [{ id: 'pr', label: 'Pull request', hint: 'has one' }, { id: 'gone', label: 'Gone' }],
    activeChips: ['pr'],
    machines: [{ id: 'm1', label: 'mac' }],
    activeMachines: [],
    ...extra,
  }
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement, tool: fixture.componentInstance }
}

describe('ListToolbar', () => {
  it('shows the count, the chips with their pressed state and hints, and the machines', async () => {
    const { root } = await render()
    expect(text(root.querySelector('.count'))).toBe('3 of 10')
    const chips = [...root.querySelectorAll<HTMLElement>('[aria-label="Quick filters"] button')]
    expect(chips.map((chip) => chip.getAttribute('aria-pressed'))).toEqual(['true', 'false'])
    expect(chips[0].getAttribute('title')).toBe('has one')
    expect(chips[1].hasAttribute('title')).toBe(false)
    expect(root.querySelector('[aria-label=Machines] button')?.getAttribute('aria-pressed')).toBe('false')
  })

  it('says what was typed or toggled', async () => {
    const { root, tool } = await render()
    const edited = vi.fn()
    const chip = vi.fn()
    const machine = vi.fn()
    tool.edited.subscribe(edited)
    tool.chipToggled.subscribe(chip)
    tool.machineToggled.subscribe(machine)
    const input = root.querySelector('input') as HTMLInputElement
    input.value = 'fix'
    input.dispatchEvent(new Event('input'))
    ;(root.querySelector('[aria-label="Quick filters"] button') as HTMLElement).click()
    ;(root.querySelector('[aria-label=Machines] button') as HTMLElement).click()
    expect(edited).toHaveBeenCalledWith('fix')
    expect(chip).toHaveBeenCalledWith('pr')
    expect(machine).toHaveBeenCalledWith('m1')
  })

  it('has no machine chips for none, clears and focuses its box, and reports whether it is empty', async () => {
    const { fixture, root, tool } = await render({ machines: [], text: 'zeta' })
    expect(root.querySelector('[aria-label=Machines]')).toBeNull()
    const edited = vi.fn()
    tool.edited.subscribe(edited)
    expect(tool.element).toBe(root.querySelector('input'))
    expect(tool.element.value).toBe('zeta')
    expect(tool.isEmpty()).toBe(false)
    tool.focus()
    expect(document.activeElement).toBe(tool.element)
    ;(root.querySelector('button[aria-label="Clear the filter text"]') as HTMLElement).click()
    expect(edited).toHaveBeenCalledWith('')
    expect(tool.isEmpty()).toBe(true)
    fixture.componentRef.setInput('text', '')
    await fixture.whenStable()
    expect(root.querySelector('button[aria-label="Clear the filter text"]')).toBeNull()
  })

  it('explains the grammar on request, naming the fields', async () => {
    const { fixture, root } = await render()
    const help = root.querySelector('button[aria-label="How the filter works"]') as HTMLElement
    expect(root.querySelector('.hint')).toBeNull()
    help.click()
    await fixture.whenStable()
    const hint = root.querySelector('.hint') as HTMLElement
    expect(help.getAttribute('aria-expanded')).toBe('true')
    expect(root.querySelector('input')?.getAttribute('aria-describedby')).toBe(hint.id)
    expect(text(hint)).toContain('sneat-*/*-go')
    expect(text(hint)).toContain('branch, repo')
    help.click()
    await fixture.whenStable()
    expect(root.querySelector('.hint')).toBeNull()
  })
})
