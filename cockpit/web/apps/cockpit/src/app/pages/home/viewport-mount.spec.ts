import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { VIEWPORT_MARGIN, ViewportMount } from './viewport-mount'

@Component({ selector: 'app-probe', template: '<b>{{ word() }}</b>', changeDetection: ChangeDetectionStrategy.OnPush })
class Probe {
  readonly word = input.required<string>()
}

const settle = () => new Promise((done) => setTimeout(done, 0))

// Fixtures attach their elements to the document; each test starts with an empty one.
beforeEach(() => {
  document.body.innerHTML = ''
})

describe('ViewportMount', () => {
  const real = window.IntersectionObserver
  let observed: Element[]
  let report: (intersecting: boolean) => void
  let disconnects: number

  function fakeObserver(): void {
    observed = []
    disconnects = 0
    window.IntersectionObserver = class {
      constructor(
        callback: IntersectionObserverCallback,
        readonly options?: IntersectionObserverInit,
      ) {
        report = (isIntersecting) => callback([{ isIntersecting } as IntersectionObserverEntry], this as unknown as IntersectionObserver)
        expect(options?.rootMargin).toBe(`${VIEWPORT_MARGIN}px`)
      }
      observe(element: Element): void {
        observed.push(element)
      }
      disconnect(): void {
        disconnects++
      }
    } as unknown as typeof IntersectionObserver
  }

  afterEach(() => {
    window.IntersectionObserver = real
    document.body.innerHTML = ''
  })

  it('keeps its place, and loads the component only when the place nears the viewport', async () => {
    fakeObserver()
    const load = vi.fn().mockResolvedValue(Probe)
    const fixture = TestBed.createComponent(ViewportMount)
    fixture.componentRef.setInput('load', load)
    fixture.componentRef.setInput('inputs', { word: 'charts' })
    await fixture.whenStable()
    expect(observed).toHaveLength(1)
    expect(observed[0].classList.contains('home-lazy-slot')).toBe(true)
    report(false)
    await settle()
    expect(load).not.toHaveBeenCalled()
    report(true)
    await settle()
    await fixture.whenStable()
    expect(load).toHaveBeenCalledTimes(1)
    expect(disconnects).toBeGreaterThan(0)
    expect(fixture.nativeElement.querySelector('.home-lazy-slot')).toBeNull()
    expect(fixture.nativeElement.parentElement.querySelector('app-probe b')?.textContent).toBe('charts')
    fixture.componentRef.setInput('inputs', { word: 'again' })
    await fixture.whenStable()
    expect(fixture.nativeElement.parentElement.querySelector('app-probe b')?.textContent).toBe('again')
  })

  it('loads at once where there is no IntersectionObserver', async () => {
    window.IntersectionObserver = undefined as unknown as typeof IntersectionObserver
    const load = vi.fn().mockResolvedValue(Probe)
    const fixture = TestBed.createComponent(ViewportMount)
    fixture.componentRef.setInput('load', load)
    fixture.componentRef.setInput('inputs', { word: 'now' })
    await fixture.whenStable()
    await settle()
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('leaves nothing behind when the chunk cannot be fetched or the page has gone, and lets go of the observer', async () => {
    fakeObserver()
    const failing = TestBed.createComponent(ViewportMount)
    failing.componentRef.setInput('load', () => Promise.reject(new Error('offline')))
    await failing.whenStable()
    report(true)
    await settle()
    const gone = TestBed.createComponent(ViewportMount)
    gone.componentRef.setInput('load', () => settle().then(() => Probe))
    await gone.whenStable()
    report(true)
    gone.destroy()
    await settle()
    await settle()
    expect(document.querySelectorAll('app-probe')).toHaveLength(0)
    const idle = TestBed.createComponent(ViewportMount)
    idle.componentRef.setInput('load', () => Promise.resolve(Probe))
    await idle.whenStable()
    const before = disconnects
    idle.destroy()
    expect(disconnects).toBe(before + 1)
  })
})
