import { TestBed } from '@angular/core/testing'
import { PaletteResult } from './palette-search'
import { PaletteRecents, RECENTS_KEY, RECENTS_LIMIT, RECENTS_STORAGE, defaultStorage, parseRecents } from './recents'

const result = (id: string, extra: Partial<PaletteResult> = {}): PaletteResult => ({
  id: `task:${id}`,
  kind: 'task',
  label: id,
  detail: 'working',
  link: { path: '/tasks/detail', query: { task: id } },
  ...extra,
})

class MemoryStorage {
  data = new Map<string, string>()
  getItem = (key: string) => this.data.get(key) ?? null
  setItem = (key: string, value: string) => void this.data.set(key, value)
}

function recentsWith(storage: unknown): PaletteRecents {
  TestBed.configureTestingModule({ providers: [{ provide: RECENTS_STORAGE, useValue: storage }] })
  return TestBed.inject(PaletteRecents)
}

describe('defaultStorage', () => {
  it('is the local storage of the window, or null when it is missing or refuses access', () => {
    expect(defaultStorage(window)).toBe(window.localStorage)
    expect(defaultStorage(null)).toBeNull()
    expect(defaultStorage({} as Window)).toBeNull()
    expect(
      defaultStorage({
        get localStorage(): Storage {
          throw new Error('blocked')
        },
      } as Window),
    ).toBeNull()
  })

  it('is what the application injects', () => {
    expect(TestBed.inject(RECENTS_STORAGE)).toBe(window.localStorage)
  })
})

describe('parseRecents', () => {
  it('keeps well-formed results', () => {
    expect(parseRecents(JSON.stringify([result('a'), result('b', { link: { path: '/', query: {} } })])).map((item) => item.label)).toEqual(['a', 'b'])
  })

  it('drops everything that is not a well-formed result', () => {
    const bad: unknown[] = [
      null,
      'text',
      { ...result('a'), id: 1 },
      { ...result('b'), kind: 'nonsense' },
      { ...result('c'), kind: 4 },
      { ...result('d'), label: 'x'.repeat(301) },
      { ...result('e'), detail: null },
      { ...result('f'), link: null },
      { ...result('g'), link: { path: 'https://evil.example/', query: {} } },
      { ...result('h'), link: { path: '//evil.example', query: {} } },
      { ...result('i'), link: { path: '/a/../b', query: {} } },
      { ...result('j'), link: { path: '/ok', query: null } },
      { ...result('k'), link: { path: '/ok', query: ['x'] } },
      { ...result('l'), link: { path: '/ok', query: { a: 1 } } },
      { ...result('m'), link: { path: '/ok', query: Object.fromEntries(Array.from({ length: 9 }, (_, i) => [`q${i}`, 'v'])) } },
      { ...result('n'), link: { path: '/ok', query: { ['k'.repeat(41)]: 'v' } } },
    ]
    expect(parseRecents(JSON.stringify([...bad, result('kept')])).map((item) => item.label)).toEqual(['kept'])
  })

  it('reads nothing from a missing, broken or non-list value, and caps the list', () => {
    expect(parseRecents(null)).toEqual([])
    expect(parseRecents('{not json')).toEqual([])
    expect(parseRecents('{"a":1}')).toEqual([])
    expect(parseRecents(JSON.stringify(Array.from({ length: 10 }, (_, i) => result(`t${i}`)))).length).toBe(RECENTS_LIMIT)
  })
})

describe('PaletteRecents', () => {
  it('starts from what is stored, puts the newest first without a duplicate, and keeps six', () => {
    const storage = new MemoryStorage()
    storage.setItem(RECENTS_KEY, JSON.stringify([result('old')]))
    const recents = recentsWith(storage)
    expect(recents.items().map((item) => item.label)).toEqual(['old'])
    for (const name of ['a', 'b', 'c', 'd', 'e', 'f', 'old']) recents.add(result(name))
    expect(recents.items().map((item) => item.label)).toEqual(['old', 'f', 'e', 'd', 'c', 'b'])
    expect(JSON.parse(storage.data.get(RECENTS_KEY) as string)).toHaveLength(RECENTS_LIMIT)
  })

  it('works without any storage', () => {
    const recents = recentsWith(null)
    recents.add(result('a'))
    expect(recents.items()).toHaveLength(1)
  })

  it('keeps the recents of this session when the store refuses to read or write', () => {
    const recents = recentsWith({
      getItem: () => {
        throw new Error('blocked')
      },
      setItem: () => {
        throw new Error('full')
      },
    })
    expect(recents.items()).toEqual([])
    recents.add(result('a'))
    expect(recents.items()).toHaveLength(1)
  })
})
