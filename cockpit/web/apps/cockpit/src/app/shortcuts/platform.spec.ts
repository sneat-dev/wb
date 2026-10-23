import { modifierLabel } from './platform'

describe('modifierLabel', () => {
  it('is the command key on Apple platforms and Ctrl elsewhere', () => {
    expect(modifierLabel({ platform: 'MacIntel', userAgent: 'Mozilla' })).toBe('⌘')
    expect(modifierLabel({ platform: '', userAgent: 'Mozilla (iPhone)' })).toBe('⌘')
    expect(modifierLabel({ platform: 'Win32', userAgent: 'Mozilla' })).toBe('Ctrl')
    expect(modifierLabel(undefined)).toBe('Ctrl')
  })
})
