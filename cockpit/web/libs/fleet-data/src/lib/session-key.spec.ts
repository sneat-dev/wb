import { DOCUMENT } from '@angular/common'
import { TestBed } from '@angular/core/testing'
import { FETCH } from './fleet-client'
import { LOGIN_KEY, SESSION_KEY_HEADER, SESSION_KEY_STORAGE, SESSION_KEY_STORE, SessionKeys, sessionKeyStorage, takeLoginKey } from './session-key'

const KEY = 'k'.repeat(43)
const OTHER = 'o'.repeat(43)

/** A page address and its history, as `takeLoginKey` sees them. */
function address(url: string) {
  const parsed = new URL(url)
  const replaceState = vi.fn<(state: unknown, unused: string, url: string) => void>()
  return { win: { location: parsed as unknown as Location, history: { state: { navigationId: 1 }, replaceState } as unknown as History }, replaceState }
}

/** A storage that holds what it is given, or refuses every call. */
function storage(broken = false): Storage {
  const held = new Map<string, string>()
  const refuse = () => {
    throw new Error('storage is blocked')
  }
  return {
    getItem: broken ? refuse : (name: string) => held.get(name) ?? null,
    setItem: broken ? refuse : (name: string, value: string) => void held.set(name, value),
    removeItem: broken ? refuse : (name: string) => void held.delete(name),
  } as unknown as Storage
}

function keysWith(store: Storage | null): SessionKeys {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: SESSION_KEY_STORE, useValue: store }] })
  return TestBed.inject(SessionKeys)
}

// cockpit#ac:session-key-reaches-the-page-in-the-fragment
describe('takeLoginKey', () => {
  it('takes the key from the fragment of the page on a loopback host and removes the fragment, keeping the path, the query and the history state', () => {
    for (const host of ['127.0.0.1:8766', '[::1]:8766', 'localhost:4300', '127.0.0.1']) {
      const { win, replaceState } = address(`http://${host}/cockpit/tasks?machine=m1#key=${KEY}`)
      expect(takeLoginKey(win)).toBe(KEY)
      expect(replaceState).toHaveBeenCalledExactlyOnceWith({ navigationId: 1 }, '', '/cockpit/tasks?machine=m1')
    }
  })

  it('leaves an address with no key in its fragment as it is', () => {
    for (const url of ['http://127.0.0.1:8766/cockpit/', 'http://127.0.0.1:8766/cockpit/#', 'http://127.0.0.1:8766/cockpit/#section', 'http://127.0.0.1:8766/cockpit/?key=' + KEY]) {
      const { win, replaceState } = address(url)
      expect(takeLoginKey(win)).toBeNull()
      expect(replaceState).not.toHaveBeenCalled()
    }
  })

  it('removes a malformed key from the address and offers nothing', () => {
    for (const bad of ['', 'short', KEY + 'x', KEY.slice(1) + '!', KEY.slice(1) + '%20']) {
      const { win, replaceState } = address(`http://127.0.0.1:8766/cockpit/#key=${bad}`)
      expect(takeLoginKey(win)).toBeNull()
      expect(replaceState).toHaveBeenCalledExactlyOnceWith({ navigationId: 1 }, '', '/cockpit/')
    }
  })

  // The hosted page must never receive or use the key.
  it('removes the key from the address of a page that is not on a loopback host and never offers it', () => {
    for (const url of [`https://sneat.dev/wb/cockpit/#key=${KEY}`, `http://127.0.0.2:8766/cockpit/#key=${KEY}`, `http://127.0.0.1.attacker.example/cockpit/#key=${KEY}`]) {
      const { win, replaceState } = address(url)
      expect(takeLoginKey(win)).toBeNull()
      expect(replaceState).toHaveBeenCalledTimes(1)
    }
  })
})

describe('sessionKeyStorage', () => {
  it('is the local storage of the window, or null where there is no window or the storage is blocked', () => {
    const local = storage()
    expect(sessionKeyStorage({ localStorage: local } as Window)).toBe(local)
    expect(sessionKeyStorage(null)).toBeNull()
    const blocked = Object.defineProperty({}, 'localStorage', {
      get: () => {
        throw new Error('blocked')
      },
    }) as Window
    expect(sessionKeyStorage(blocked)).toBeNull()
  })

  it('is what the store token is made of, and no key is offered by default', () => {
    const local = storage()
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: DOCUMENT, useValue: { defaultView: { localStorage: local } } }] })
    expect(TestBed.inject(SESSION_KEY_STORE)).toBe(local)
    expect(TestBed.inject(LOGIN_KEY)).toBeNull()
  })
})

describe('SessionKeys', () => {
  it('holds no key until one is adopted, keeps it in the storage of the origin, and forgets it when dropped', () => {
    const store = storage()
    const keys = keysWith(store)
    expect(keys.current()).toBeNull()
    keys.adopt(KEY)
    expect(keys.current()).toBe(KEY)
    expect(store.getItem(SESSION_KEY_STORAGE)).toBe(KEY)
    keys.drop(KEY)
    expect(keys.current()).toBeNull()
    expect(store.getItem(SESSION_KEY_STORAGE)).toBeNull()
  })

  // cockpit#ac:a-reload-and-a-second-tab-keep-the-owner-session
  it('reads the key another tab of the origin stored, and the one it replaced it with', () => {
    const store = storage()
    const first = keysWith(store)
    first.adopt(KEY)
    const second = keysWith(store)
    expect(second.current()).toBe(KEY)
    second.adopt(OTHER)
    expect(first.current()).toBe(OTHER)
  })

  it('does not drop a key that another tab has replaced since', () => {
    const store = storage()
    const keys = keysWith(store)
    keys.adopt(KEY)
    store.setItem(SESSION_KEY_STORAGE, OTHER)
    keys.drop(KEY)
    expect(keys.current()).toBe(OTHER)
  })

  it('sends nothing that is stored but is not a well-formed key', () => {
    const store = storage()
    store.setItem(SESSION_KEY_STORAGE, 'not a key\r\nX-Injected: 1')
    expect(keysWith(store).current()).toBeNull()
  })

  it('keeps the key in this tab alone where there is no storage or the storage refuses', () => {
    for (const store of [null, storage(true)]) {
      const keys = keysWith(store)
      expect(keys.current()).toBeNull()
      keys.adopt(KEY)
      expect(keys.current()).toBe(KEY)
      keys.drop(OTHER)
      expect(keys.current()).toBe(KEY)
      keys.drop(KEY)
      expect(keys.current()).toBeNull()
    }
  })

  describe('sign', () => {
    const sent = (init: RequestInit | undefined) => new Headers(init?.headers).get(SESSION_KEY_HEADER)

    it('adds the key to a request to a path of the own origin, keeping the other options and headers', () => {
      const keys = keysWith(storage())
      keys.adopt(KEY)
      const signal = AbortSignal.timeout(1000)
      const init = keys.sign('/api/v1/cockpit/session', { headers: { Accept: 'application/json' }, credentials: 'same-origin', signal })
      expect(sent(init)).toBe(KEY)
      expect(new Headers(init?.headers).get('Accept')).toBe('application/json')
      expect(init).toMatchObject({ credentials: 'same-origin', signal })
      expect(sent(keys.sign('/api/v1/log'))).toBe(KEY)
      expect(sent(keys.sign('/'))).toBe(KEY)
    })

    it('leaves a request alone when no key is held', () => {
      const init = { headers: { Accept: 'application/json' } }
      expect(keysWith(storage()).sign('/api/v1/cockpit/session', init)).toBe(init)
      expect(keysWith(storage()).sign('/api/v1/cockpit/session')).toBeUndefined()
    })

    it('keeps the key a request names itself', () => {
      const keys = keysWith(storage())
      keys.adopt(KEY)
      expect(sent(keys.sign('/api/v1/cockpit/session', { headers: { [SESSION_KEY_HEADER]: OTHER } }))).toBe(OTHER)
    })

    // The key goes to the daemon's own origin and nowhere else.
    it('never adds the key to a request to another address', () => {
      const keys = keysWith(storage())
      keys.adopt(KEY)
      const init = { headers: { Accept: 'application/json' } }
      for (const input of ['https://sneat.dev/wb/cockpit/api', 'http://127.0.0.1:3000/api/v1/cockpit/session', '//attacker.example/api', '/\\attacker.example/api', '\\\\attacker.example/api', '/\\/attacker.example', 'api/relative', '', new URL('http://127.0.0.1:8766/api/v1/cockpit/session'), new Request('http://127.0.0.1:8766/api/v1/cockpit/session')]) {
        expect(keys.sign(input, init)).toBe(init)
      }
    })
  })

  it('is what the default fetch signs every request with', async () => {
    const global = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{}'))
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: SESSION_KEY_STORE, useValue: storage() }] })
    TestBed.inject(SessionKeys).adopt(KEY)
    const fetcher = TestBed.inject(FETCH)
    await fetcher('/api/v1/cockpit/fleet', { headers: { Accept: 'application/json' } })
    await fetcher('https://elsewhere.example/', { headers: { Accept: 'application/json' } })
    const [own, foreign] = global.mock.calls as unknown as [string, RequestInit][]
    expect(own[0]).toBe('/api/v1/cockpit/fleet')
    expect(new Headers(own[1].headers).get(SESSION_KEY_HEADER)).toBe(KEY)
    expect(new Headers(foreign[1].headers).has(SESSION_KEY_HEADER)).toBe(false)
    global.mockRestore()
  })
})
