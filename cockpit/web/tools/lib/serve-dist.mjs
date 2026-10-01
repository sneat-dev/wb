// A static server for the end-to-end test: it serves dist/ under /cockpit/ the
// way the wb daemon does (cockpit/web/embed.go), for what a browser test can
// observe. It mirrors:
//   - only GET and HEAD are answered; anything else is 405 with Allow;
//   - every response carries the strict Content-Security-Policy, and the entry
//     document gets a fresh nonce in the policy and in place of __CSP_NONCE__;
//   - a missing path whose extension has a known content type is 404, any other
//     missing path (or a directory) falls back to the entry document;
//   - the entry document and the fallback are Cache-Control: no-cache;
//   - .gitkeep is never served;
//   - the content types of .html .css .js .mjs .json .map .svg .woff2 .woff
//     .png .ico, application/octet-stream for other existing files.
// It does not mirror the "not built" page. A Go test keeps the policy text
// below identical to embed.go's PolicyFor.
import { randomBytes } from 'node:crypto'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { basename, extname, join, resolve, sep } from 'node:path'

const mount = '/cockpit/'

export const contentTypes = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json',
  '.map': 'application/json',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.png': 'image/png',
  '.ico': 'image/vnd.microsoft.icon',
}

export function policy(nonce) {
  return `default-src 'none'; script-src 'self'; style-src 'self' 'nonce-${nonce}'; img-src 'self'; font-src 'self'; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'`
}

export function createHandler(distRoot) {
  const root = resolve(distRoot)
  return (request, response) => {
    const nonce = randomBytes(16).toString('hex')
    response.setHeader('Content-Security-Policy', policy(nonce))
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      response.writeHead(405, { Allow: 'GET, HEAD' }).end('method not allowed')
      return
    }
    const { pathname } = new URL(request.url, 'http://localhost')
    if (!pathname.startsWith(mount)) {
      response.writeHead(404).end('not found')
      return
    }
    let file = resolve(join(root, decodeURIComponent(pathname.slice(mount.length))))
    const inside = file === root || file.startsWith(root + sep)
    const isFile = inside && existsSync(file) && statSync(file).isFile() && basename(file) !== '.gitkeep'
    if (!isFile) {
      if (extname(file) in contentTypes || basename(file) === '.gitkeep') {
        response.writeHead(404).end('not found')
        return
      }
      file = join(root, 'index.html')
    }
    const entry = file === join(root, 'index.html')
    let body = readFileSync(file)
    if (entry) body = Buffer.from(body.toString().replaceAll('__CSP_NONCE__', nonce))
    const headers = { 'Content-Type': contentTypes[extname(file)] ?? 'application/octet-stream' }
    if (!isFile || entry) headers['Cache-Control'] = 'no-cache'
    response.writeHead(200, headers)
    response.end(request.method === 'HEAD' ? undefined : body)
  }
}
