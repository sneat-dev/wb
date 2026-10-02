import { Component, ViewContainerRef } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { OVERLAYS_IMPORT, OVERLAY_RETRY_MS, OverlayLoader } from './overlay-loader'
import { ShellState } from './shell-state'

@Component({ template: 'overlays' })
class Fake {}

describe('OverlayLoader', () => {
  const importer = vi.fn()

  function host(): ViewContainerRef {
    @Component({ template: '' })
    class Host {
      readonly ref = TestBed.runInInjectionContext(() => TestBed.inject(ViewContainerRef, null as never, { optional: true }))
    }
    return TestBed.createComponent(Host).componentRef.injector.get(ViewContainerRef)
  }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout'] })
    importer.mockReset().mockResolvedValue({ Overlays: Fake })
    TestBed.configureTestingModule({ providers: [{ provide: OVERLAYS_IMPORT, useValue: importer }] })
  })
  afterEach(() => vi.useRealTimers())

  it('fetches once and puts the overlays in the host', async () => {
    const loader = TestBed.inject(OverlayLoader)
    const ref = host()
    loader.attach(ref)
    await Promise.all([loader.ensure(), loader.ensure()])
    expect(importer).toHaveBeenCalledTimes(1)
    expect(ref.length).toBe(1)
    await loader.ensure()
    expect(ref.length).toBe(1)
  })

  it('creates them when the host arrives after the fetch, and not again', async () => {
    const loader = TestBed.inject(OverlayLoader)
    await loader.ensure()
    const ref = host()
    const detach = loader.attach(ref)
    expect(ref.length).toBe(1)
    detach()
    loader.attach(ref)
    expect(ref.length).toBe(1)
  })

  it('creates nothing once the host is let go', async () => {
    const loader = TestBed.inject(OverlayLoader)
    const ref = host()
    loader.attach(ref)()
    await loader.ensure()
    expect(ref.length).toBe(0)
  })

  it('retries a failed fetch once', async () => {
    importer.mockRejectedValueOnce(new Error('offline'))
    const loader = TestBed.inject(OverlayLoader)
    const ref = host()
    loader.attach(ref)
    const done = loader.ensure()
    await vi.advanceTimersByTimeAsync(OVERLAY_RETRY_MS)
    await done
    expect(importer).toHaveBeenCalledTimes(2)
    expect(ref.length).toBe(1)
  })

  it('closes what was opened when the retry fails too, and starts afresh next time', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    importer.mockRejectedValue(new Error('offline'))
    const shell = TestBed.inject(ShellState)
    shell.openPalette()
    shell.toggleOwnerHint()
    const loader = TestBed.inject(OverlayLoader)
    const done = loader.ensure()
    await vi.advanceTimersByTimeAsync(OVERLAY_RETRY_MS)
    await done
    expect(shell.modalOpen()).toBe(false)
    // The sign-in card is closed too: it is in the chunk that did not arrive.
    expect(shell.ownerHintOpen()).toBe(false)
    expect(error).toHaveBeenCalled()
    importer.mockResolvedValue({ Overlays: Fake })
    await loader.ensure()
    expect(importer).toHaveBeenCalledTimes(3)
    error.mockRestore()
  })

  it('imports the real overlays by default', async () => {
    TestBed.resetTestingModule()
    const { Overlays } = await TestBed.inject(OVERLAYS_IMPORT)()
    expect(typeof Overlays).toBe('function')
  })
})
