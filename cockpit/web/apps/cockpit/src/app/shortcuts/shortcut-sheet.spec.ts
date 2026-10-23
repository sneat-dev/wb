import { TestBed } from '@angular/core/testing'
import { ShellState } from '../shell/shell-state'
import { ShortcutSheet } from './shortcut-sheet'

describe('ShortcutSheet', () => {
  async function render() {
    const fixture = TestBed.createComponent(ShortcutSheet)
    await fixture.whenStable()
    return { fixture, root: fixture.nativeElement as HTMLElement, shell: TestBed.inject(ShellState) }
  }

  it('renders nothing while closed', async () => {
    const { root } = await render()
    expect(root.querySelector('[role="dialog"]')).toBeNull()
  })

  it('lists every shortcut when opened, with the modifier of the platform', async () => {
    const { fixture, root, shell } = await render()
    shell.openSheet()
    await fixture.whenStable()
    const dialog = root.querySelector('[role="dialog"]') as HTMLElement
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.querySelector('h2')?.textContent).toBe('Keyboard shortcuts')
    const rows = [...dialog.querySelectorAll('.row')].map((row) => [[...row.querySelectorAll('dt kbd, dt .then')].map((part) => part.textContent).join(' '), row.querySelector('dd')?.textContent])
    expect(rows.slice(0, 6)).toEqual([
      ['g then h', 'Home'],
      ['g then t', 'Tasks'],
      ['g then r', 'Repositories'],
      ['g then w', 'Worktrees'],
      ['g then a', 'Agents'],
      ['g then m', 'Machines'],
    ])
    expect(rows.map((row) => row[0])).toEqual(expect.arrayContaining(['/', 'j', 'k', 'Enter', 'Esc', '?']))
    expect(rows.some((row) => /^(⌘|Ctrl) K$/.test(row[0] ?? ''))).toBe(true)
  })

  it('closes with its button and with a click on the backdrop', async () => {
    const { fixture, root, shell } = await render()
    shell.openSheet()
    await fixture.whenStable()
    ;(root.querySelector('button[aria-label="Close"]') as HTMLButtonElement).click()
    await fixture.whenStable()
    expect(shell.sheetOpen()).toBe(false)
    shell.openSheet()
    await fixture.whenStable()
    ;(root.querySelector('.overlay-backdrop') as HTMLElement).click()
    expect(shell.sheetOpen()).toBe(false)
  })
})
