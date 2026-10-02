// The session key (cockpit#req:session-key): the half of an owner session that only this page holds.
//
// The session cookie is scoped to the loopback host, not to the daemon's port, so the browser sends it to every
// other server on that host, and such a server could replay it. The key is what it cannot have: `wb cockpit`
// prints it in the login URL's fragment (which no server is sent), the page takes it from the address at once,
// keeps it in the storage of its own origin (an origin includes the port, so a page of another local server
// cannot read it) and sends it as a request header on every request to the daemon. Without it the cookie is
// an anonymous reader's.
import { DOCUMENT } from '@angular/common'
import { Injectable, InjectionToken, inject } from '@angular/core'

/** The request header that carries the key. */
export const SESSION_KEY_HEADER = 'X-Wb-Cockpit-Session-Key'

/**
 * Where the key is kept: the local storage of the page's own origin. Every tab of the origin reads it, so a
 * reload and a second tab are the owner's too; no other origin can, whatever its port.
 */
export const SESSION_KEY_STORAGE = 'wb-cockpit.session-key'

/** The name the key has in the login URL's fragment: `/cockpit/session/login?code=<code>#key=<key>`. */
export const LOGIN_KEY_FRAGMENT = 'key'

/** A key is 256 random bits in unpadded URL-safe base64. */
const KEY_SHAPE = /^[A-Za-z0-9_-]{43}$/

/**
 * A path of the page's own origin: one slash and then anything but a second slash or a backslash. A browser
 * reads `//host` and `/\host` alike as an address on another host.
 */
const OWN_PATH = /^\/(?![/\\])/

/** The host names the daemon's own page is served on. The hosted page is on none of them and never takes a key. */
const LOOPBACK_HOSTS: ReadonlySet<string> = new Set(['127.0.0.1', '[::1]', 'localhost'])

/**
 * Takes the key out of the page's address, before anything else reads the address: a fragment that names a
 * key is removed from the address bar and from this history entry, whatever it holds, and the key is returned
 * when the page is the daemon's own (a loopback host) and the key is well formed. It is only an offer: the
 * store keeps it once the daemon has answered that it opens an owner session (see `FleetStore`).
 */
export function takeLoginKey(win: Pick<Window, 'location' | 'history'>): string | null {
  const key = new URLSearchParams(win.location.hash.slice(1)).get(LOGIN_KEY_FRAGMENT)
  if (key === null) return null
  win.history.replaceState(win.history.state, '', win.location.pathname + win.location.search)
  return LOOPBACK_HOSTS.has(win.location.hostname) && KEY_SHAPE.test(key) ? key : null
}

/** The key the login URL offered this page load, or null: `main.ts` provides what `takeLoginKey` returned. */
export const LOGIN_KEY = new InjectionToken<string | null>('login key', { providedIn: 'root', factory: () => null })

/** The browser's local storage, or null where it is not available (a private window, blocked site data). */
export function sessionKeyStorage(win: Window | null): Storage | null {
  try {
    return win?.localStorage ?? null
  } catch {
    return null
  }
}

export const SESSION_KEY_STORE = new InjectionToken<Storage | null>('session key storage', {
  providedIn: 'root',
  factory: () => sessionKeyStorage(inject(DOCUMENT).defaultView),
})

/** The key this origin holds. Where the storage cannot be used, this tab alone holds it, until it is closed or reloaded. */
@Injectable({ providedIn: 'root' })
export class SessionKeys {
  private readonly storage = inject(SESSION_KEY_STORE)
  private memory: string | null = null

  /** The key, read from the storage every time, so a login or a logout in another tab is this tab's at once. */
  current(): string | null {
    try {
      const stored = this.storage?.getItem(SESSION_KEY_STORAGE) ?? null
      // What is stored may have been changed by anything that shares the origin: only a well-formed key is sent.
      if (stored !== null && KEY_SHAPE.test(stored)) return stored
    } catch {
      // Unreadable storage: the tab's own copy.
    }
    return this.memory
  }

  /** Keeps `key` as this origin's. */
  adopt(key: string): void {
    this.memory = key
    try {
      this.storage?.setItem(SESSION_KEY_STORAGE, key)
    } catch {
      // Unwritable storage (full, or blocked): this tab keeps the key in memory.
    }
  }

  /** Forgets `key` if it is still the one held: a key another tab has replaced since is kept. */
  drop(key: string): void {
    if (this.current() !== key) return
    this.memory = null
    try {
      this.storage?.removeItem(SESSION_KEY_STORAGE)
    } catch {
      // Nothing to remove from storage that cannot be used.
    }
  }

  /**
   * The request options with the key added, for a request to the page's own origin (a path) that names no key
   * of its own. A request to any other address is never given the key.
   */
  sign(input: RequestInfo | URL, init?: RequestInit): RequestInit | undefined {
    const key = this.current()
    if (key === null || typeof input !== 'string' || !OWN_PATH.test(input)) return init
    const headers = new Headers(init?.headers)
    if (!headers.has(SESSION_KEY_HEADER)) headers.set(SESSION_KEY_HEADER, key)
    return { ...init, headers }
  }
}
