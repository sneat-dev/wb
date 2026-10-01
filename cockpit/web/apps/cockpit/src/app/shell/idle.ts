/** How long the browser may wait for an idle moment before the callback runs anyway, in milliseconds. */
export const IDLE_TIMEOUT_MS = 2000
/** Where there is no idle callback (Safari), how long to wait instead. */
export const IDLE_FALLBACK_MS = 300

type IdleWindow = Pick<Window, 'setTimeout' | 'clearTimeout'> & Partial<Pick<Window, 'requestIdleCallback' | 'cancelIdleCallback'>>

/** Runs `run` when the browser is idle, so it never competes with the first render; the returned function cancels it. */
export function whenIdle(win: IdleWindow, run: () => void): () => void {
  const { requestIdleCallback, cancelIdleCallback } = win
  if (requestIdleCallback && cancelIdleCallback) {
    const id = requestIdleCallback.call(win, run, { timeout: IDLE_TIMEOUT_MS })
    return () => cancelIdleCallback.call(win, id)
  }
  const id = win.setTimeout(run, IDLE_FALLBACK_MS)
  return () => win.clearTimeout(id)
}
