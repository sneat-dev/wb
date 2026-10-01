import { createAppConfig, readCspNonce } from './app.config'
import { appRoutes } from './app.routes'

function documentWith(markup: string): Document {
  return new DOMParser().parseFromString(markup, 'text/html')
}

describe('readCspNonce', () => {
  it('reads the nonce from the ngCspNonce attribute', () => {
    const doc = documentWith('<app-root ngCspNonce="abc123"></app-root>')
    expect(readCspNonce(doc)).toBe('abc123')
  })

  it('is undefined when the document carries no nonce', () => {
    expect(readCspNonce(documentWith('<app-root></app-root>'))).toBeUndefined()
  })
})

describe('createAppConfig', () => {
  it('provides the router, PrimeNG and the zoneless runtime', () => {
    const config = createAppConfig(documentWith('<app-root ngCspNonce="n"></app-root>'))
    expect(config.providers.length).toBeGreaterThan(3)
    expect(appRoutes.length).toBeGreaterThan(5)
  })
})
