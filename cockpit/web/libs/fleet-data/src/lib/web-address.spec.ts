import { MAX_WEB_ADDRESS, webAddress } from './web-address'

describe('webAddress', () => {
  it('accepts an https address with a plain hostname, normalised', () => {
    expect(webAddress('https://github.com/sneat-dev/wb/pull/12')).toBe('https://github.com/sneat-dev/wb/pull/12')
    expect(webAddress('https://x.test')).toBe('https://x.test/')
    expect(webAddress('HTTPS://Git-Hub.example.com/a?b=c#d')).toBe('https://git-hub.example.com/a?b=c#d')
    expect(webAddress('https://github.com./a')).toBe('https://github.com./a')
  })

  it('refuses every other scheme and a missing, unparsable or oversized address', () => {
    for (const url of ['http://x.test/a', 'javascript:alert(1)', 'data:text/html,x', 'file:///etc/passwd', 'ftp://x.test', '//x.test/a', 'x.test/a', '', 'https://', 'https:///a']) {
      expect(webAddress(url), url).toBeUndefined()
    }
    expect(webAddress(undefined)).toBeUndefined()
    expect(webAddress(`https://x.test/${'a'.repeat(MAX_WEB_ADDRESS)}`)).toBeUndefined()
  })

  it('refuses whitespace, control and invisible characters anywhere, even the ones the URL parser would drop', () => {
    for (const url of [
      'https://x.test/a b',
      'https://x.test/a\tb',
      'https://x.te\nst/a',
      ' https://x.test',
      'https://x.test/a\u0000',
      'https://x.test/\u202e',
      'https://x.test/\u200b',
      'https://x.test/\u2028',
    ]) {
      expect(webAddress(url), JSON.stringify(url)).toBeUndefined()
    }
  })

  it('refuses credentials, a port, an IP address and a single-label or oddly spelled host', () => {
    for (const url of [
      'https://user@github.com/a',
      'https://user:pw@github.com/a',
      'https://github.com:8443/a',
      'https://github.com:443@evil.test/a',
      'https://127.0.0.1/a',
      'https://10.0.0.1/a',
      'https://[::1]/a',
      'https://2130706433/a',
      'https://localhost/a',
      'https://x_y.test/a',
      'https://-x.test/a',
      'https://x-.test/a',
      'https://x..test/a',
    ]) {
      expect(webAddress(url), url).toBeUndefined()
    }
  })
})
