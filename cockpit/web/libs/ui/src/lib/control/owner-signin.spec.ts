import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from './clipboard'
import { OwnerSignIn } from './owner-signin'

describe('OwnerSignIn', () => {
  // cockpit-views#ac:owner-gating-is-one-affordance
  it('offers "Sign in as owner: run wb cockpit" with the command ready to copy', async () => {
    const copy = vi.fn().mockResolvedValue(true)
    TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
    const fixture = TestBed.createComponent(OwnerSignIn)
    const copied: string[] = []
    fixture.componentInstance.copied.subscribe((text) => copied.push(text))
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    expect(root.querySelector('.ask')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('Sign in as owner: run wb cockpit')
    expect(root.querySelector('code')?.textContent).toBe('wb cockpit')
    ;(root.querySelector('button') as HTMLButtonElement).click()
    await fixture.whenStable()
    expect(copy).toHaveBeenCalledWith('wb cockpit')
    expect(copied).toEqual(['wb cockpit'])
  })
})
