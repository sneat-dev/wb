import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { LazyMount } from './lazy-mount'

@Component({ selector: 'app-probe', template: '<b>{{ word() }}</b>', changeDetection: ChangeDetectionStrategy.OnPush })
class Probe {
  readonly word = input.required<string>()
}

const settle = () => new Promise((done) => setTimeout(done, 0))

// Fixtures attach their elements to the document; each test starts with an empty one.
beforeEach(() => {
  document.body.innerHTML = ''
})

describe('LazyMount', () => {
  it('creates the component once its chunk has arrived, with its inputs set before it first renders', async () => {
    const load = vi.fn().mockResolvedValue(Probe)
    const fixture = TestBed.createComponent(LazyMount)
    fixture.componentRef.setInput('load', load)
    fixture.componentRef.setInput('inputs', { word: 'first' })
    await fixture.whenStable()
    await settle()
    await fixture.whenStable()
    expect(load).toHaveBeenCalledTimes(1)
    expect(fixture.nativeElement.parentElement.querySelector('app-probe b')?.textContent).toBe('first')
  })

  it('keeps the inputs current, and loads only once', async () => {
    const load = vi.fn().mockResolvedValue(Probe)
    const fixture = TestBed.createComponent(LazyMount)
    fixture.componentRef.setInput('load', load)
    fixture.componentRef.setInput('inputs', { word: 'first' })
    await fixture.whenStable()
    await settle()
    fixture.componentRef.setInput('inputs', { word: 'second' })
    fixture.componentRef.setInput('load', load.bind(null))
    await fixture.whenStable()
    await settle()
    await fixture.whenStable()
    expect(load).toHaveBeenCalledTimes(1)
    expect(document.querySelectorAll('app-probe')).toHaveLength(1)
    expect(document.querySelector('app-probe b')?.textContent).toBe('second')
  })

  it('leaves nothing behind when the chunk cannot be fetched, or when the page has gone first', async () => {
    const failing = TestBed.createComponent(LazyMount)
    failing.componentRef.setInput('load', () => Promise.reject(new Error('offline')))
    await failing.whenStable()
    await settle()
    const gone = TestBed.createComponent(LazyMount)
    gone.componentRef.setInput('load', () => settle().then(() => Probe))
    await gone.whenStable()
    gone.destroy()
    await settle()
    await settle()
    expect(document.querySelectorAll('app-probe')).toHaveLength(0)
  })
})
