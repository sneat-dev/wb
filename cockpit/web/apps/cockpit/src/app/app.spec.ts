import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { App } from './app'

describe('App', () => {
  it('renders the shell with the product name and an empty router outlet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(App)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    expect(root.querySelector('h1')?.textContent).toBe('WB Cockpit')
    expect(root.querySelector('main router-outlet')).not.toBeNull()
  })
})
