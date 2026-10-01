import { TestBed } from '@angular/core/testing'
import { PAGE_LOCATION, SchemaMismatch } from './schema-mismatch'

describe('SchemaMismatch', () => {
  function render(mismatch: 'daemon-older' | 'page-older') {
    const fixture = TestBed.createComponent(SchemaMismatch)
    fixture.componentRef.setInput('mismatch', mismatch)
    return fixture
  }

  it('tells the operator to update wb on this machine when the daemon is older, and names the command', async () => {
    const fixture = render('daemon-older')
    await fixture.whenStable()
    const root = fixture.nativeElement as HTMLElement
    expect(root.querySelector('[role="alert"]')).not.toBeNull()
    expect(root.textContent).toContain('update wb on this machine')
    expect(root.querySelector('code')?.textContent).toBe('wb self-update')
  })

  it('tells the operator to reload when the page is older', async () => {
    const fixture = render('page-older')
    await fixture.whenStable()
    const root = fixture.nativeElement as HTMLElement
    expect(root.textContent).toContain('reload')
    expect(root.textContent).not.toContain('update wb')
    expect(root.querySelector('code')).toBeNull()
  })

  it('reloads the page from its button', async () => {
    const reload = vi.fn()
    TestBed.configureTestingModule({ providers: [{ provide: PAGE_LOCATION, useValue: { reload } }] })
    const fixture = render('page-older')
    await fixture.whenStable()
    ;(fixture.nativeElement.querySelector('button') as HTMLButtonElement).click()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('reloads through the location of the document by default', () => {
    expect(TestBed.inject(PAGE_LOCATION)).toBe(document.location)
  })
})
