// The one check for a remote-supplied URL that becomes an `href` (a pull
// request's `url`, a repository's `remote_url_web`). Everything in the read
// model that is a link goes through it; a page never writes `[href]` from a
// field the daemon sent without it.

/** The longest address accepted; a longer one is not a link anyone follows. */
export const MAX_WEB_ADDRESS = 2048

// Letters, digits and hyphens in labels of 1 to 63 characters, at least two labels.
const HOSTNAME = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/i

/**
 * The normalised address when `url` is an `https:` address with a plain
 * hostname, else undefined. Refused: any other scheme (`http`, `javascript`,
 * `data`, `file`), whitespace or control characters anywhere (the URL parser
 * would silently drop some), credentials, a port, an IP address (v4 or v6), a
 * single-label host such as `localhost`, and anything over `MAX_WEB_ADDRESS`.
 */
export function webAddress(url: string | undefined): string | undefined {
  // eslint-disable-next-line no-control-regex
  if (url === undefined || url.length > MAX_WEB_ADDRESS || /[\s\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2028\u2029\u202a-\u202e\u2066-\u2069\ufeff]/.test(url)) return undefined
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return undefined
  }
  if (parsed.protocol !== 'https:' || parsed.username !== '' || parsed.password !== '' || parsed.port !== '') return undefined
  const host = parsed.hostname.replace(/\.$/, '')
  // The last label of an IPv4 address is a number; a real top-level domain never is.
  if (!HOSTNAME.test(host) || /^\d+$/.test(host.slice(host.lastIndexOf('.') + 1))) return undefined
  return parsed.href
}
