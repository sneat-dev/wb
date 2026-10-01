import { describe, expect, it } from 'vitest'
import { GALLERY_DIST, LIST_SHOTS, SCHEMES, VIEWPORTS, galleryPlan, routePlan, shotPlan } from './shot-plan.mjs'

const document = {
  repositories: [{ id: 'r0', name: 'acme/cached' }, { id: 'r1', host: 'github.com', name: 'sneat-co/sneat-go' }],
  worktrees: [{ id: 'wt 1', task: 'fix & go' }],
  agents: [{ id: 'ag/1' }],
  machines: [{ id: 'mach-mac' }],
}

describe('routePlan', () => {
  it('has every route of the application, with the first entries in the detail routes', () => {
    const plan = routePlan(document)
    expect(plan.map((route) => route.name)).toEqual(['home', 'tasks', 'tasks-new', 'task-detail', 'repositories', 'repository-by-name', 'repository-by-id', 'worktrees', 'worktree-detail', 'agents', 'agent-detail', 'machines', 'machine-detail'])
    const url = Object.fromEntries(plan.map((route) => [route.name, route.url]))
    expect(url['task-detail']).toBe('/tasks/detail?task=fix%20%26%20go')
    expect(url['repository-by-name']).toBe('/repositories/github.com/sneat-co/sneat-go')
    expect(url['repository-by-id']).toBe('/repositories/r1')
    expect(url['worktree-detail']).toBe('/worktrees/wt%201')
    expect(url['agent-detail']).toBe('/agents/ag%2F1')
    expect(url['machine-detail']).toBe('/machines/mach-mac')
  })

  it('names a repository with no host by a dash, and takes the first repository when none has a host', () => {
    const plan = routePlan({ ...document, repositories: [{ id: 'r0', name: 'acme/cached' }] })
    expect(plan.find((route) => route.name === 'repository-by-name')?.url).toBe('/repositories/-/acme/cached')
  })
})

describe('shotPlan', () => {
  const shots = shotPlan(document)

  it('photographs each route in light and dark at a desktop, a phone and the narrowest size', () => {
    expect(VIEWPORTS.map((viewport) => `${viewport.width}x${viewport.height}`)).toEqual(['1440x900', '390x844', '360x800'])
    expect(SCHEMES).toEqual(['light', 'dark'])
    const routes = shots.filter((shot) => shot.state === 'ok' && !shot.keys && !shot.steps)
    expect(routes).toHaveLength(13 * 2 * 3)
    expect(routes.map((shot) => shot.file)).toContain('home-dark-390.png')
    expect(routes.map((shot) => shot.file)).toContain('machine-detail-light-1440.png')
  })

  it('adds the palette, its results, the shortcut sheet and the states of the daemon', () => {
    expect(shots.filter((shot) => shot.keys).map((shot) => shot.file).sort()).toEqual(
      ['palette', 'palette-results', 'shortcuts'].flatMap((name) => ['dark', 'light'].flatMap((scheme) => ['1440', '390', '360'].map((size) => `${name}-${scheme}-${size}.png`))).sort(),
    )
    expect(shots.filter((shot) => shot.state !== 'ok').map((shot) => shot.file).sort()).toEqual(
      ['daemon-older', 'warming'].flatMap((state) => ['dark', 'light'].flatMap((scheme) => ['1440', '390', '360'].map((size) => `state-${state}-${scheme}-${size}.png`))).sort(),
    )
  })

  it('adds the list and its side panel on Worktrees, each with the steps that reach it', () => {
    const lists = shots.filter((shot) => shot.steps)
    expect(lists).toHaveLength(LIST_SHOTS.length * 2 * 3)
    expect(new Set(lists.map((shot) => shot.url))).toEqual(new Set(['/worktrees']))
    expect(lists.map((shot) => shot.file)).toContain('worktrees-panel-raw-dark-390.png')
    expect(LIST_SHOTS.find((shot) => shot.name === 'worktrees-panel-raw')?.steps).toEqual(['row:2', 'raw'])
  })

  it('gives every shot a distinct file', () => {
    expect(new Set(shots.map((shot) => shot.file)).size).toBe(shots.length)
  })
})

describe('galleryPlan', () => {
  it('photographs the whole gallery from the preview build, once the charts have drawn, in each scheme and size', () => {
    const shots = galleryPlan()
    expect(GALLERY_DIST).toBe('dist-preview')
    expect(shots.map((shot) => shot.file).sort()).toEqual(['gallery-dark-1440.png', 'gallery-dark-360.png', 'gallery-dark-390.png', 'gallery-light-1440.png', 'gallery-light-360.png', 'gallery-light-390.png'])
    for (const shot of shots) expect(shot).toMatchObject({ url: '/gallery', state: 'ok', dist: 'dist-preview', fullPage: true, ready: 'canvas' })
  })
})
