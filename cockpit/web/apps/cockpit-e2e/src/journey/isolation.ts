import { spawnSync } from 'node:child_process'
import { createServer } from 'node:net'
import { relative, isAbsolute, resolve } from 'node:path'

// The journey starts a real wb daemon. These guards keep it off the machine's
// own: the founder's daemon listens on 8766 and keeps its state under the real
// home, so the journey never uses that port and never inherits a variable that
// could point at real state.

/** The default daemon address of every machine; the journey must never touch it. */
export const DEFAULT_DAEMON_PORT = 8766

/** Names the environment variables a child may inherit; everything else is dropped. */
const INHERITED = ['PATH', 'LANG', 'LC_ALL']

export function assertSafePort(port: number): void {
  if (!Number.isInteger(port) || port <= 0 || port > 65535) throw new Error(`journey: ${port} is not a usable port`)
  if (port === DEFAULT_DAEMON_PORT) throw new Error(`journey: refusing port ${DEFAULT_DAEMON_PORT}, the default daemon port`)
}

export function assertInside(path: string, root: string): void {
  const inside = relative(resolve(root), resolve(path))
  if (inside === '' || inside.startsWith('..') || isAbsolute(inside)) throw new Error(`journey: ${path} is not inside ${root}`)
}

/**
 * The whole environment of every wb process the journey starts, built from
 * nothing: only PATH and the locale pass through, so no WB_*, XDG_*, HOME,
 * token or other variable of the real machine can reach the daemon.
 */
export function isolatedEnv(tmp: string, inherited: NodeJS.ProcessEnv): Record<string, string> {
  const env: Record<string, string> = {}
  for (const name of INHERITED) {
    const value = inherited[name]
    if (value) env[name] = value
  }
  const home = `${tmp}/home`
  return {
    ...env,
    HOME: home,
    XDG_CONFIG_HOME: `${home}/config`,
    XDG_STATE_HOME: `${home}/state`,
    XDG_CACHE_HOME: `${home}/cache`,
    XDG_DATA_HOME: `${home}/data`,
    TMPDIR: `${tmp}/tmp`,
    WB_PROJECTS_ROOT: `${tmp}/projects`,
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_TERMINAL_PROMPT: '0',
    GIT_AUTHOR_NAME: 'Journey',
    GIT_AUTHOR_EMAIL: 'journey@example.test',
    GIT_COMMITTER_NAME: 'Journey',
    GIT_COMMITTER_EMAIL: 'journey@example.test',
  }
}

/** A loopback port that is free right now and is not the default daemon port. */
export async function freePort(attempts = 20): Promise<number> {
  for (let attempt = 0; attempt < attempts; attempt++) {
    const port = await new Promise<number>((done, fail) => {
      const server = createServer()
      server.once('error', fail)
      server.listen(0, '127.0.0.1', () => {
        const { port } = server.address() as { port: number }
        server.close(() => done(port))
      })
    })
    if (port !== DEFAULT_DAEMON_PORT) return port
  }
  throw new Error('journey: no free port other than the default daemon port')
}

/** Whether something accepts connections on the loopback port. */
export function portInUse(port: number): Promise<boolean> {
  return new Promise((done) => {
    const server = createServer()
    server.once('error', () => done(true))
    server.listen(port, '127.0.0.1', () => server.close(() => done(false)))
  })
}

/** Whether the loopback port is in use, answered synchronously for exit handlers. */
export function portInUseSync(port: number): boolean {
  const probe = `require('node:net').createServer().once('error',()=>process.exit(1)).listen(${Number(port)},'127.0.0.1',function(){this.close(()=>process.exit(0))})`
  return spawnSync(process.execPath, ['-e', probe], { timeout: 10_000 }).status !== 0
}

/**
 * The pid recorded in the daemon's state file, only when it is safe to kill:
 * the record says the daemon is not stopped and its port is still in use. A
 * stopped daemon's pid may have been recycled by an unrelated process.
 */
export function recordedLivePid(stateText: string, portBusy: boolean): number | undefined {
  if (!portBusy) return undefined
  let state: { pid?: unknown; status?: unknown }
  try {
    state = JSON.parse(stateText)
  } catch {
    return undefined
  }
  const { pid, status } = state
  return typeof pid === 'number' && pid > 1 && typeof status === 'string' && status !== 'stopped' ? pid : undefined
}
