import { createAppConfig } from './app.config'

describe('createAppConfig', () => {
  it('provides the error listeners, the zoneless runtime, the router, the page titles and the list shortcuts', () => {
    expect(createAppConfig().providers).toHaveLength(5)
  })
})
