import { LOGIN_KEY } from '@cockpit/fleet-data'
import { createAppConfig } from './app.config'

describe('createAppConfig', () => {
  it('provides the error listeners, the zoneless runtime, the router, the page titles, the list shortcuts and the login key', () => {
    expect(createAppConfig().providers).toHaveLength(6)
  })

  it('provides the session key the login URL offered, and none by default', () => {
    expect(createAppConfig().providers.at(-1)).toEqual({ provide: LOGIN_KEY, useValue: null })
    expect(createAppConfig('k').providers.at(-1)).toEqual({ provide: LOGIN_KEY, useValue: 'k' })
  })
})
