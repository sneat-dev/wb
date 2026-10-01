/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  CopyCommand,
  PICKABLE_REPOSITORY,
  PLACEHOLDERS,
  agentDispatch,
  agentLogs,
  agentStatus,
  agentStop,
  branchCleanup,
  branchList,
  cockpitExport,
  daemonStart,
  fleetStatus,
  newTaskCommands,
  pickRepositories,
  pullRequestCreate,
  pullRequestLand,
  remoteEnroll,
  remotePublish,
  selfUpdate,
  sessionSend,
  shellQuote,
  valueProblem,
  worktreeCleanup,
  worktreeCreate,
  worktreeList,
} from './commands'

const text = (command: CopyCommand): string => {
  if (!command.ok) throw new Error(`refused: ${command.reason}`)
  return command.text
}

/** Splits a command text into shell words, undoing single quotes (and the `'\''` escape). */
function shellWords(line: string): string[] {
  const words: string[] = []
  let current = ''
  let started = false
  let quoted = false
  for (let index = 0; index < line.length; index++) {
    const char = line[index]
    if (quoted) {
      if (char === "'") quoted = false
      else current += char
    } else if (char === "'") {
      quoted = true
      started = true
    } else if (char === '\\' && line[index + 1] === "'") {
      current += "'"
      index++
    } else if (char === ' ') {
      if (started) words.push(current)
      current = ''
      started = false
    } else {
      current += char
      started = true
    }
  }
  if (started) words.push(current)
  return words
}

interface Manifest {
  capabilities: { surfaces: { runtime?: { commands?: { path: string; flags?: string[] }[] } } }[]
}

const manifest = JSON.parse(readFileSync(resolve(import.meta.dirname, '../../../../../../ai/capabilities.json'), 'utf8')) as Manifest
const commands = new Map<string, Set<string>>()
for (const capability of manifest.capabilities) {
  for (const command of capability.surfaces.runtime?.commands ?? []) commands.set(command.path, new Set(command.flags ?? []))
}

/** The flags a verb requires, which the manifest does not record: from REQ:copy-the-command. */
const REQUIRED_FLAGS: Record<string, string[]> = {
  'wb worktree create': ['--model', '--original-prompt-file'],
  'wb pr create': ['--commit-all', '--message'],
  'wb branch cleanup': ['--repo', '--branch'],
  'wb agent dispatch': ['--repo', '--task', '--profile', '--new-worktree'],
  'wb remote enroll': ['--url', '--token-stdin'],
}

/** The verbs of the export, which the export task adds to the manifest: tolerated as pending only while absent. */
const PENDING_VERBS = new Set(['wb cockpit export'])

/** Finds the command path of a template: the longest prefix of its words that is a manifest path. */
function verbOf(words: string[]): string | undefined {
  for (let length = Math.min(words.length, 4); length > 0; length--) {
    const candidate = words.slice(0, length).join(' ')
    if (commands.has(candidate) || PENDING_VERBS.has(candidate)) return candidate
  }
  return undefined
}

const TEMPLATES: Record<string, CopyCommand> = {
  worktreeList: worktreeList('fix-ci'),
  pullRequestCreate: pullRequestCreate('fix-ci'),
  worktreeCleanup: worktreeCleanup('fix-ci'),
  pullRequestLand: pullRequestLand('sneat-dev/wb', 12),
  worktreeCreate: worktreeCreate('fix-ci', ['sneat-dev/wb', 'sneat-co/sneat-go']),
  worktreeCreateFull: worktreeCreate('fix-ci', ['sneat-dev/wb'], { model: 'opus', promptFile: 'p.md', base: 'main' }),
  branchList: branchList('sneat-dev/wb'),
  branchListOne: branchList('sneat-dev/wb', 'topic'),
  fleetStatus: fleetStatus('sneat-dev/wb'),
  branchCleanup: branchCleanup('sneat-dev/wb', 'topic'),
  agentStatus: agentStatus('run-1'),
  agentLogs: agentLogs('run-1'),
  agentStop: agentStop('run-1'),
  sessionSend: sessionSend('wb-session-1'),
  agentDispatch: agentDispatch('sneat-dev/wb', 'fix-ci'),
  agentDispatchBase: agentDispatch('sneat-dev/wb', 'fix-ci', { base: 'main', profile: 'deep' }),
  remotePublish: remotePublish(),
  selfUpdate: selfUpdate(),
  daemonStart: daemonStart(),
  remoteEnroll: remoteEnroll(),
  cockpitExport: cockpitExport(),
}

describe('Copy command templates against the command manifest', () => {
  // cockpit-views#ac:copy-command-templates-match-the-manifest
  it.each(Object.entries(TEMPLATES))('%s: its verb and every flag exist, and the flags it requires are present', (_name, command) => {
    const words = shellWords(text(command))
    const verb = verbOf(words)
    expect(verb, `no manifest command for: ${text(command)}`).toBeDefined()
    const flags = words.filter((word) => word.startsWith('--')).map((word) => word.split('=')[0])
    if (verb !== undefined && commands.has(verb)) {
      for (const flag of flags) expect(commands.get(verb)?.has(flag), `${verb} has no ${flag}`).toBe(true)
    }
    for (const required of REQUIRED_FLAGS[verb ?? ''] ?? []) expect(flags).toContain(required)
    // Never the destructive form, and never a filesystem path.
    expect(text(command)).not.toContain('--apply')
    expect(text(command)).not.toMatch(/(^| )(\/|~|\.\.?\/)/)
  })

  it('requires the flags it names to exist in the manifest too', () => {
    for (const [verb, flags] of Object.entries(REQUIRED_FLAGS)) {
      for (const flag of flags) expect(commands.get(verb)?.has(flag), `${verb} ${flag}`).toBe(true)
    }
  })

  it('lists a verb as pending only while the manifest lacks it', () => {
    for (const verb of PENDING_VERBS) {
      // When the export task adds the verb, delete it from PENDING_VERBS and add its flags above.
      expect(commands.has(verb)).toBe(false)
    }
  })

  it('offers no template for the verbs the manifest does not have', () => {
    expect(commands.has('wb worktree path')).toBe(false)
    expect(commands.has('wb worktree open')).toBe(false)
  })
})

describe('Copy command texts', () => {
  // cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers
  it('are exactly the commands of REQ:copy-the-command, values single-quoted and flags --flag=value', () => {
    expect(text(worktreeList('fix-ci'))).toBe("wb worktree list 'fix-ci'")
    expect(text(pullRequestCreate('fix-ci'))).toBe("wb pr create 'fix-ci' --commit-all --message='<message>'")
    expect(text(pullRequestCreate('fix-ci', 'ship it'))).toBe("wb pr create 'fix-ci' --commit-all --message='ship it'")
    expect(text(worktreeCleanup('fix-ci'))).toBe("wb worktree cleanup 'fix-ci'")
    expect(text(pullRequestLand('sneat-dev/wb', 12))).toBe("wb pr land 'sneat-dev/wb#12'")
    expect(text(worktreeCreate('<task>', ['<owner/repository>']))).toBe(
      "wb worktree create '<task>' '<owner/repository>' --model='<model>' --original-prompt-file='<file>'",
    )
    expect(text(branchList('sneat-dev/wb'))).toBe("wb branch list --repo='sneat-dev/wb'")
    expect(text(branchList('sneat-dev/wb', 'topic'))).toBe("wb branch list --repo='sneat-dev/wb' --branch='topic'")
    expect(text(fleetStatus('sneat-dev/wb'))).toBe("wb fleet status --filter='sneat-dev/wb'")
    expect(text(branchCleanup('sneat-dev/wb', 'topic'))).toBe("wb branch cleanup --repo='sneat-dev/wb' --branch='topic'")
    expect(text(agentStatus('run-1'))).toBe("wb agent status 'run-1'")
    expect(text(agentLogs('run-1'))).toBe("wb agent logs 'run-1'")
    expect(text(agentStop('run-1'))).toBe("wb agent stop 'run-1'")
    expect(text(sessionSend('s-1'))).toBe("wb session send 's-1' --message='<message>'")
    expect(text(sessionSend('s-1', 'hi'))).toBe("wb session send 's-1' --message='hi'")
    expect(text(remotePublish())).toBe('wb remote publish')
    expect(text(selfUpdate())).toBe('wb self-update')
    expect(text(daemonStart())).toBe('wb daemon start')
    expect(text(remoteEnroll())).toBe("wb remote enroll --url='<hub-url>' --token-stdin")
    expect(text(remoteEnroll('https://hub.example'))).toBe("wb remote enroll --url='https://hub.example' --token-stdin")
    expect(text(cockpitExport())).toBe("wb cockpit export --format='json'")
    expect(PLACEHOLDERS.promptFile).toBe('<file>')
  })

  it('label the command for a machine without an SSH route "run on <machine>", and not for this one', () => {
    expect(worktreeList('fix-ci', { machine: 'old' })).toEqual({ ok: true, text: "wb worktree list 'fix-ci'", label: 'run on old' })
    expect(worktreeList('fix-ci', {})).toEqual({ ok: true, text: "wb worktree list 'fix-ci'" })
  })

  // cockpit-views#ac:copy-command-for-an-ssh-machine
  it('go through ssh for a machine with an SSH route, the interpolated value quoted', () => {
    const vm = { machine: 'vm', ssh: { host: 'vm.example', user: 'alex', wbPath: '/usr/local/bin/wb' } }
    expect(text(worktreeList('fix-ci', vm))).toBe("ssh alex@vm.example /usr/local/bin/wb worktree list 'fix-ci'")
    expect(text(worktreeList('fix-ci', { ssh: { host: 'vm.example', user: 'alex' } }))).toBe("ssh alex@vm.example wb worktree list 'fix-ci'")
    // The remote shell splits the arguments again, so a value that is not shell-safe is quoted twice.
    expect(text(pullRequestCreate('fix ci', 'a b', { ssh: { host: 'h', user: 'u' } }))).toBe(
      "ssh u@h wb pr create ''\\''fix ci'\\''' --commit-all --message=''\\''a b'\\'''",
    )
    expect(shellWords(text(worktreeList("it's", { ssh: { host: 'h', user: 'u' } }))).slice(0, 4)).toEqual(['ssh', 'u@h', 'wb', 'worktree'])
    expect(text(worktreeList('x', { ssh: { host: 'h h', user: 'u', wbPath: '/opt/my wb' } }))).toBe("ssh 'u@h h' ''\\''/opt/my wb'\\''' worktree list 'x'")
  })

  it('refuse a route whose host, user or path is hostile', () => {
    for (const ssh of [{ host: '-oProxyCommand=x', user: 'u' }, { host: 'h', user: 'u\nv' }, { host: 'h', user: 'u', wbPath: '-x' }]) {
      const result = worktreeList('t', { ssh })
      expect(result.ok).toBe(false)
    }
  })

  // cockpit-views#ac:copy-command-refuses-hostile-values
  it('single-quotes a value with a quote and escapes the quote', () => {
    const hostile = "a'; rm -rf ~; '"
    expect(text(worktreeList(hostile))).toBe("wb worktree list 'a'\\''; rm -rf ~; '\\'''")
    expect(shellWords(text(worktreeList(hostile)))).toEqual(['wb', 'worktree', 'list', hostile])
    expect(shellQuote("it's")).toBe("'it'\\''s'")
    expect(shellQuote('plain')).toBe("'plain'")
  })

  it('refuses a value that starts with a dash or holds a control character, saying why, and copies nothing', () => {
    for (const value of ['-x', '--upstream', 'a\nb', 'a\u0000b', 'a\u007fb', 'a\u202eb', 'a\u200fb']) {
      for (const command of [worktreeList(value), branchList('o/r', value), pullRequestCreate('t', value), agentStop(value), worktreeCreate('t', ['o/r'], { model: value })]) {
        expect(command.ok).toBe(false)
        expect(command.ok ? '' : command.reason.length).toBeGreaterThan(10)
      }
    }
    expect(valueProblem('')).toMatch(/empty/)
    expect(valueProblem('-x')).toMatch(/starts with/)
    expect(valueProblem('a\nb')).toMatch(/control character/)
    expect(valueProblem('fine')).toBeUndefined()
  })
})

describe('the New task form', () => {
  it('offers only names that are owner/name of safe characters, narrowed by the wildcard matcher', () => {
    const names = ['sneat-co/sneat-go', 'sneat-co/bots-go', 'owner/na me', 'owner/ok.name', 'sneat-dev/wb', 'nested/a/b', 'o/r;x']
    expect(pickRepositories(names, 'sneat-*/*-go', 0)).toEqual(['sneat-co/sneat-go', 'sneat-co/bots-go'])
    expect(pickRepositories(names, 'ok', 0)).toEqual(['owner/ok.name'])
    expect(pickRepositories(names, '', 0)).not.toContain('owner/na me')
    expect(pickRepositories(names, '', 0)).not.toContain('nested/a/b')
    expect(pickRepositories(names, '-sneat', 0)).toEqual(['owner/ok.name', 'o/r;x'].filter((name) => PICKABLE_REPOSITORY.test(name)))
  })

  // cockpit-views#ac:new-task-form-produces-commands
  it('produces the creation command and one dispatch command per repository', () => {
    const commands = newTaskCommands({ task: 'fix-ci', repositories: ['sneat-co/sneat-go', 'sneat-co/bots-go'], base: 'main', model: 'opus' })
    expect(text(commands.create)).toBe(
      "wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file='<file>' --base='main'",
    )
    expect(commands.dispatch.map(text)).toEqual([
      "wb agent dispatch --repo='sneat-co/sneat-go' --task='fix-ci' --profile='<profile>' --new-worktree='fix-ci' --base='main'",
      "wb agent dispatch --repo='sneat-co/bots-go' --task='fix-ci' --profile='<profile>' --new-worktree='fix-ci' --base='main'",
    ])
    expect(commands.dispatch.map(text).join('')).not.toContain('--model')
    expect(text(newTaskCommands({ task: 't', repositories: ['o/r'], model: 'unknown' }).create)).not.toContain('--base')
  })

  it('refuses to produce a command until a model is entered, a repository is chosen and every value is safe', () => {
    const refusal = (form: Parameters<typeof newTaskCommands>[0]): string => {
      const commands = newTaskCommands(form)
      expect(commands.create.ok).toBe(false)
      expect(commands.dispatch.every((command) => !command.ok)).toBe(true)
      return commands.create.ok ? '' : commands.create.reason
    }
    expect(refusal({ task: 'fix-ci', repositories: ['o/r'], model: '' })).toMatch(/model is required/)
    expect(refusal({ task: 'fix-ci', repositories: ['o/r'], model: '  ' })).toMatch(/model is required/)
    expect(refusal({ task: 'fix-ci', repositories: [], model: 'opus' })).toMatch(/at least one repository/)
    expect(refusal({ task: 'fix-ci', repositories: ['owner/na me'], model: 'opus' })).toMatch(/not an owner\/name/)
    // A refused task value is refused by the command itself.
    const hostileTask = newTaskCommands({ task: '-x', repositories: ['o/r'], model: 'opus' })
    expect(hostileTask.create.ok).toBe(false)
    expect(hostileTask.dispatch[0].ok).toBe(false)
  })
})
