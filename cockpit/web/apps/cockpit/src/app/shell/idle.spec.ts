import { IDLE_FALLBACK_MS, IDLE_TIMEOUT_MS, whenIdle } from './idle'

describe('whenIdle', () => {
  it('asks the browser for an idle moment, with a timeout, and can cancel', () => {
    const run = vi.fn()
    const requestIdleCallback = vi.fn(() => 7)
    const cancelIdleCallback = vi.fn()
    const cancel = whenIdle({ requestIdleCallback, cancelIdleCallback, setTimeout, clearTimeout } as never, run)
    expect(requestIdleCallback).toHaveBeenCalledWith(run, { timeout: IDLE_TIMEOUT_MS })
    cancel()
    expect(cancelIdleCallback).toHaveBeenCalledWith(7)
  })

  it('falls back to a short timer where there is no idle callback, and can cancel', () => {
    const run = vi.fn()
    const win = { setTimeout: vi.fn(() => 9), clearTimeout: vi.fn() }
    const cancel = whenIdle(win as never, run)
    expect(win.setTimeout).toHaveBeenCalledWith(run, IDLE_FALLBACK_MS)
    cancel()
    expect(win.clearTimeout).toHaveBeenCalledWith(9)
  })
})
