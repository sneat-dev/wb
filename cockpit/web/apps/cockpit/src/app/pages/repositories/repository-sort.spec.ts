import { SORT_PRESETS, activePreset } from './repository-sort'

describe('sort presets', () => {
  it('are Recent, Most worktrees and Most branches, which sort by activity, worktrees and branches', () => {
    expect(SORT_PRESETS.map((preset) => [preset.label, preset.id])).toEqual([
      ['Recent', 'activity'],
      ['Most worktrees', 'worktrees'],
      ['Most branches', 'branches'],
    ])
  })

  it('is active for a descending sort by its column, whoever chose it, and for no other order', () => {
    expect(activePreset({ sort: 'worktrees', dir: 'desc' })).toBe('worktrees')
    expect(activePreset({ sort: 'activity', dir: 'desc' })).toBe('activity')
    expect(activePreset({ sort: 'worktrees', dir: 'asc' })).toBeUndefined()
    expect(activePreset({ sort: 'repository', dir: 'desc' })).toBeUndefined()
  })
})
