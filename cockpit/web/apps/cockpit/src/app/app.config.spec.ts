import { createAppConfig } from './app.config'
import { readCspNonce } from './csp-nonce'

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
  it('provides the error listeners, the zoneless runtime and the router; PrimeNG comes with the lazy pages', () => {
    expect(createAppConfig().providers).toHaveLength(5)
  })
})
