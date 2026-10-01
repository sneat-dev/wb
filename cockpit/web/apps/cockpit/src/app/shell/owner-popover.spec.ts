import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from '@cockpit/ui'
import { OwnerPopover } from './owner-popover'
import { ShellState } from './shell-state'

describe('OwnerPopover', () => {
  async function render() {
    const copy = vi.fn().mockResolvedValue(true)
    TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
    const chip = document.createElement('button')
    chip.className = 'session'
    document.body.appendChild(chip)
    const fixture = TestBed.createComponent(OwnerPopover)
    document.body.appendChild(fixture.nativeElement)
    const shell = TestBed.inject(ShellState)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    return { fixture, shell, chip, root, copy, card: () => root.querySelector<HTMLElement>('[role="dialog"]') }
  }

  afterEach(() => {
    document.body.innerHTML = ''
  })

  // cockpit-views#ac:owner-gating-is-one-affordance
  it('shows nothing until opened, then "Sign in as owner: run wb cockpit" with the command to copy, and takes focus', async () => {
    const { fixture, shell, root, card, copy } = await render()
    expect(card()).toBeNull()
    shell.toggleOwnerHint()
    await fixture.whenStable()
    expect(card()?.getAttribute('aria-label')).toBe('Sign in as owner')
    expect(root.querySelector('.ask')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('Sign in as owner: run wb cockpit')
    expect(document.activeElement).toBe(card())
    ;(root.querySelector('button') as HTMLButtonElement).click()
    await fixture.whenStable()
    expect(copy).toHaveBeenCalledWith('wb cockpit')
  })

  it('closes on Escape and returns focus to the chip; Escape while closed does nothing', async () => {
    const { fixture, shell, chip, card } = await render()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    shell.toggleOwnerHint()
    await fixture.whenStable()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await fixture.whenStable()
    expect(card()).toBeNull()
    expect(document.activeElement).toBe(chip)
  })

  it('closes on a click elsewhere, and stays open for a click inside it or on the chip, which toggles it', async () => {
    const { fixture, shell, chip, card, root } = await render()
    document.body.click()
    expect(shell.ownerHintOpen()).toBe(false)
    shell.toggleOwnerHint()
    await fixture.whenStable()
    card()?.click()
    root.querySelector<HTMLElement>('code')?.click()
    chip.click()
    expect(shell.ownerHintOpen()).toBe(true)
    document.body.click()
    await fixture.whenStable()
    expect(card()).toBeNull()
  })
})
