/// <reference types="node" />
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  CommandTarget,
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
  remotePublishDryRun,
  remoteStatus,
  selfUpdate,
  sessionList,
  sessionSend,
  shellQuote,
  valueProblem,
  worktreeCleanup,
  worktreeCreate,
  worktreeGc,
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
  capabilities: { surfaces: { runtime?: { commands?: { path: string; flags?: string[]; modes?: string[] }[] } } }[]
}

const manifest = JSON.parse(readFileSync(resolve(import.meta.dirname, '../../../../../../ai/capabilities.json'), 'utf8')) as Manifest
const commands = new Map<string, Set<string>>()
/** The flags the manifest's own text says a verb requires: a clause that ends "<flag> is required". */
const derivedRequired = new Map<string, Set<string>>()
for (const capability of manifest.capabilities) {
  for (const command of capability.surfaces.runtime?.commands ?? []) {
    commands.set(command.path, new Set(command.flags ?? []))
    for (const mode of command.modes ?? []) {
      for (const clause of mode.split(/[;,]/)) {
        const match = /(--[a-z][a-z-]*) is required(?: with any one)?$/.exec(clause.trim())
        if (match) derivedRequired.set(command.path, (derivedRequired.get(command.path) ?? new Set()).add(match[1]))
      }
    }
  }
}

/**
 * The flags a verb requires where the manifest does not say so (from
 * REQ:copy-the-command); where it does, the test below derives them and checks
 * this table agrees.
 */
const REQUIRED_FLAGS: Record<string, string[]> = {
  'wb worktree create': ['--model', '--original-prompt-file'],
  'wb pr create': ['--commit-all', '--message'],
  'wb branch cleanup': ['--repo', '--branch'],
  'wb agent dispatch': ['--repo', '--task', '--profile', '--new-worktree'],
  'wb remote enroll': ['--url', '--token-stdin'],
}

/**
 * Verbs a template may name before the manifest has them: tolerated as pending only while absent.
 * `wb cockpit export` was pending until the export verb landed (cockpit-views task 7) and is now a manifest command.
 */
const PENDING_VERBS = new Set<string>()

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
  worktreeGc: worktreeGc(),
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
  sessionList: sessionList(),
  agentDispatch: agentDispatch('sneat-dev/wb', 'fix-ci'),
  agentDispatchBase: agentDispatch('sneat-dev/wb', 'fix-ci', { base: 'main', profile: 'deep', brief: 'do it' }),
  worktreeListSsh: worktreeList('fix-ci', { ssh: { host: 'h', user: 'u' } }),
  remotePublish: remotePublish(),
  remotePublishDryRun: remotePublishDryRun(),
  remoteStatus: remoteStatus(),
  selfUpdate: selfUpdate(),
  daemonStart: daemonStart(),
  remoteEnroll: remoteEnroll(),
  cockpitExport: cockpitExport(),
}

describe('Copy command templates against the command manifest', () => {
  // cockpit-views#ac:copy-command-templates-match-the-manifest
  it.each(Object.entries(TEMPLATES))('%s: its verb and every flag exist, and the flags it requires are present', (_name, command) => {
    const parsed = shellWords(text(command))
    // The ssh form runs the same verb on the remote: judge what follows the destination and the executable.
    const words = parsed[0] === 'ssh' ? ['wb', ...parsed.slice(3)] : parsed
    const verb = verbOf(words)
    expect(verb, `no manifest command for: ${text(command)}`).toBeDefined()
    const flags = words.filter((word) => word.startsWith('--')).map((word) => word.split('=')[0])
    if (verb !== undefined && commands.has(verb)) {
      for (const flag of flags) expect(commands.get(verb)?.has(flag), `${verb} has no ${flag}`).toBe(true)
    }
    for (const required of [...(REQUIRED_FLAGS[verb ?? ''] ?? []), ...(derivedRequired.get(verb ?? '') ?? [])]) expect(flags).toContain(required)
    // Never the destructive form, and never a filesystem path.
    expect(text(command)).not.toContain('--apply')
    expect(text(command)).not.toMatch(/(^| )(\/|~|\.\.?\/)/)
  })

  it('derives required flags from the manifest where it states them, and the table agrees', () => {
    expect([...(derivedRequired.get('wb pr create') ?? [])]).toEqual(['--message'])
    for (const [verb, flags] of derivedRequired) {
      for (const flag of flags) expect(commands.get(verb)?.has(flag), `${verb} ${flag}`).toBe(true)
      // Conditional wording ("--apply is required for every deletion") is not an unconditional requirement.
      expect(flags.has('--apply')).toBe(false)
    }
    expect(REQUIRED_FLAGS['wb pr create']).toContain('--message')
  })

  it('requires the flags it names to exist in the manifest too', () => {
    for (const [verb, flags] of Object.entries(REQUIRED_FLAGS)) {
      for (const flag of flags) expect(commands.get(verb)?.has(flag), `${verb} ${flag}`).toBe(true)
    }
  })

  it('lists a verb as pending only while the manifest lacks it', () => {
    for (const verb of PENDING_VERBS) {
      // When a verb lands in the manifest, delete it from PENDING_VERBS.
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
    expect(text(pullRequestCreate('fix-ci'))).toBe("wb pr create 'fix-ci' --commit-all --message=<<<edit:message>>>")
    expect(text(pullRequestCreate('fix-ci', 'ship it'))).toBe("wb pr create 'fix-ci' --commit-all --message='ship it'")
    expect(text(worktreeCleanup('fix-ci'))).toBe("wb worktree cleanup 'fix-ci'")
    expect(text(worktreeGc())).toBe('wb worktree gc')
    expect(text(worktreeGc({ machine: 'vm' }))).toBe('wb worktree gc')
    expect(text(pullRequestLand('sneat-dev/wb', 12))).toBe("wb pr land 'sneat-dev/wb#12'")
    expect(text(worktreeCreate(PLACEHOLDERS.task, ['<owner/repository>']))).toBe(
      "wb worktree create <<<edit:task>>> '<owner/repository>' --model=<<<edit:model>>> --original-prompt-file=<<<edit:file>>>",
    )
    expect(text(branchList('sneat-dev/wb'))).toBe("wb branch list --repo='sneat-dev/wb'")
    expect(text(branchList('sneat-dev/wb', 'topic'))).toBe("wb branch list --repo='sneat-dev/wb' --branch='topic'")
    expect(text(fleetStatus('sneat-dev/wb'))).toBe("wb fleet status --filter='sneat-dev/wb'")
    expect(text(branchCleanup('sneat-dev/wb', 'topic'))).toBe("wb branch cleanup --repo='sneat-dev/wb' --branch='topic'")
    expect(text(agentStatus('run-1'))).toBe("wb agent status 'run-1'")
    expect(text(agentLogs('run-1'))).toBe("wb agent logs 'run-1'")
    expect(text(agentStop('run-1'))).toBe("wb agent stop 'run-1'")
    expect(text(sessionSend('s-1'))).toBe("wb session send 's-1' --message=<<<edit:message>>>")
    expect(text(sessionSend('s-1', 'hi'))).toBe("wb session send 's-1' --message='hi'")
    expect(text(remotePublish())).toBe('wb remote publish')
    expect(text(selfUpdate())).toBe('wb self-update')
    expect(text(daemonStart())).toBe('wb daemon start')
    expect(text(remoteEnroll())).toBe('wb remote enroll --url=<<<edit:hub-url>>> --token-stdin')
    expect(text(remoteEnroll('https://hub.example'))).toBe("wb remote enroll --url='https://hub.example' --token-stdin")
    expect(text(cockpitExport())).toBe("wb cockpit export --format='json'")
    expect(PLACEHOLDERS.promptFile).toBe('<<<edit:file>>>')
  })

  // cockpit-views#ac:copy-command-refuses-hostile-values
  it('leaves a placeholder bare and flags the entry needsEdit, so an unedited paste fails in the shell', () => {
    expect(pullRequestCreate('t')).toEqual({ ok: true, text: "wb pr create 't' --commit-all --message=<<<edit:message>>>", needsEdit: true })
    expect(pullRequestCreate('t', 'done')).toEqual({ ok: true, text: "wb pr create 't' --commit-all --message='done'", needsEdit: false })
    expect(worktreeList('t')).toMatchObject({ needsEdit: false })
    expect(worktreeCreate('t', ['o/r'])).toMatchObject({ needsEdit: true })
    expect(worktreeCreate('t', ['o/r'], { model: 'opus', promptFile: 'p.md' })).toMatchObject({ needsEdit: false })
    expect(agentDispatch('o/r', 't', { brief: 'do it' })).toMatchObject({ needsEdit: true })
    expect(text(agentDispatch('o/r', 't', { profile: 'deep', brief: 'do it' }))).toBe("wb agent dispatch --repo='o/r' --task='do it' --profile='deep' --new-worktree='t'")
    // Also through ssh: a bare placeholder is a redirection in the remote shell, which fails.
    expect(text(worktreeCreate('t', ['o/r'], { promptFile: 'p.md' }, { ssh: { host: 'h', user: 'u' } }))).toBe("ssh u@h wb worktree create 't' 'o/r' --model=<<<edit:model>>> --original-prompt-file='p.md'")
    expect(worktreeCreate('t', ['o/r'], {}, { ssh: { host: 'h' } })).toMatchObject({ needsEdit: true })
  })

  it('label the command for a machine without an SSH route "run on <machine>", and not for this one', () => {
    expect(worktreeList('fix-ci', { machine: 'old' })).toEqual({ ok: true, text: "wb worktree list 'fix-ci'", label: 'run on old', needsEdit: false })
    expect(worktreeList('fix-ci', {})).toEqual({ ok: true, text: "wb worktree list 'fix-ci'", needsEdit: false })
  })

  // cockpit-views#ac:copy-command-for-an-ssh-machine
  it('go through ssh for a machine with an SSH route, the interpolated value quoted', () => {
    const vm = { machine: 'vm', ssh: { host: 'vm.example', user: 'alex', wbPath: '/usr/local/bin/wb' } }
    expect(text(worktreeList('fix-ci', vm))).toBe("ssh alex@vm.example /usr/local/bin/wb worktree list 'fix-ci'")
    expect(text(worktreeList('fix-ci', { ssh: { host: 'vm.example', user: 'alex' } }))).toBe("ssh alex@vm.example wb worktree list 'fix-ci'")
    // The remote shell splits the arguments again, so a value that is not shell-safe is quoted twice.
    expect(text(worktreeCreate('fix ci', ['o/r'], { model: 'a b', promptFile: 'p.md' }, { ssh: { host: 'h', user: 'u' } }))).toBe(
      "ssh u@h wb worktree create ''\\''fix ci'\\''' 'o/r' --model=''\\''a b'\\''' --original-prompt-file='p.md'",
    )
    // The user may be empty: the destination is then just the host.
    expect(text(worktreeList('x', { ssh: { host: 'vm.example', user: '' } }))).toBe("ssh vm.example wb worktree list 'x'")
    expect(text(worktreeList('x', { ssh: { host: 'vm.example', user: undefined, wbPath: '' } }))).toBe("ssh vm.example wb worktree list 'x'")
    // A value that could be read as a bare =-word is quoted for the remote shell too.
    expect(text(worktreeList('=x', { ssh: { host: 'h', user: 'u' } }))).toBe("ssh u@h wb worktree list ''\\''=x'\\'''")
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
    for (const value of ['-x', '--upstream', 'a\nb', 'a\u0000b', 'a\u007fb', 'a\u202eb', 'a\u200fb', 'a\u061cb', 'a\u200bb', 'a\u200cb', 'a\u200db', 'a\ufeffb', 'a\u2028b', 'a\u2029b']) {
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
  it('produces one dispatch command per repository, with the brief as the task text and the profile of the form, and no creation command: dispatch creates the worktree', () => {
    const brief = "Fix the flaky CI.\nIt's the go-ci test job."
    const commands = newTaskCommands({ task: 'fix-ci', brief, repositories: ['sneat-co/sneat-go', 'sneat-co/bots-go'], base: 'main', model: 'opus', profile: 'cheap-coder' })
    expect(commands.create).toBeUndefined()
    expect(commands.dispatch.map(text)).toEqual([
      "wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.\nIt'\\''s the go-ci test job.' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'",
      "wb agent dispatch --repo='sneat-co/bots-go' --task='Fix the flaky CI.\nIt'\\''s the go-ci test job.' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'",
    ])
    expect(commands.dispatch).toEqual([expect.objectContaining({ needsEdit: false }), expect.objectContaining({ needsEdit: false })])
    // --task carries the brief, and the task name goes to the worktree.
    expect(commands.dispatch.map(text).join('')).not.toContain("--task='fix-ci'")
    expect(commands.dispatch.map(text).join('')).not.toContain('--model')
    // No profile in the form: the one placeholder left, for what the page cannot know.
    expect(text(newTaskCommands({ task: 't', brief: 'b', repositories: ['o/r'], model: 'unknown' }).dispatch[0])).toBe("wb agent dispatch --repo='o/r' --task='b' --profile=<<<edit:profile>>> --new-worktree='t'")
    expect(text(newTaskCommands({ task: 't', brief: 'b', repositories: ['o/r'], model: '', profile: ' ' }).dispatch[0])).toContain('--profile=')
  })

  // cockpit-views#ac:new-task-form-produces-commands
  it('produces the creation command alone, with the model, while there is no brief: a creation before a dispatch would collide with the worktree dispatch creates', () => {
    const commands = newTaskCommands({ task: 'fix-ci', brief: '  ', repositories: ['sneat-co/sneat-go', 'sneat-co/bots-go'], base: 'main', model: 'opus' })
    expect(text(commands.create as CopyCommand)).toBe(
      "wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'",
    )
    expect(commands.create).toMatchObject({ needsEdit: true })
    expect(commands.dispatch).toEqual([])
    expect(text(newTaskCommands({ task: 't', brief: '', repositories: ['o/r'], model: 'unknown' }).create as CopyCommand)).not.toContain('--base')
  })

  it('runs every command of the form on its target, when it has one', () => {
    const target = { machine: 'vm', ssh: { host: 'vm.example', user: 'me' } }
    const dispatch = newTaskCommands({ task: 't', brief: 'b', repositories: ['o/r', 'o/s'], model: 'm', target })
    expect(dispatch.dispatch.map(text).every((line) => line.startsWith('ssh me@vm.example '))).toBe(true)
    expect(dispatch.dispatch).toHaveLength(2)
    expect(text(newTaskCommands({ task: 't', brief: '', repositories: ['o/r'], model: 'm', target }).create as CopyCommand)).toMatch(/^ssh me@vm.example wb worktree create/)
    expect(text(newTaskCommands({ task: 't', brief: '', repositories: ['o/r'], model: 'm', target: {} }).create as CopyCommand)).toMatch(/^wb worktree create/)
  })

  it('accepts a multi-line brief but refuses control characters and a leading dash in it', () => {
    const refused = (brief: string) => agentDispatch('o/r', 't', { brief }).ok
    expect(refused('line one\nline two\tindented')).toBe(true)
    expect(refused('-x')).toBe(false)
    expect(refused('bad\u0000')).toBe(false)
    expect(refused('bad ')).toBe(false)
    expect(valueProblem('a\nb', true)).toBeUndefined()
    expect(valueProblem('a\nb')).toMatch(/control/)
  })

  it('refuses to produce a command until a repository is chosen and every value is safe, and, without a brief, until a model is entered', () => {
    const refusal = (form: Parameters<typeof newTaskCommands>[0]): string => {
      const commands = newTaskCommands(form)
      expect(commands.create?.ok).toBe(false)
      expect(commands.dispatch.every((command) => !command.ok)).toBe(true)
      return commands.create?.ok === false ? commands.create.reason : ''
    }
    expect(refusal({ task: 'fix-ci', brief: '', repositories: ['o/r'], model: '' })).toMatch(/model is required/)
    expect(refusal({ task: 'fix-ci', brief: ' ', repositories: ['o/r'], model: '  ' })).toMatch(/model is required/)
    expect(refusal({ task: 'fix-ci', brief: 'b', repositories: [], model: 'opus' })).toMatch(/at least one repository/)
    expect(refusal({ task: 'fix-ci', brief: 'b', repositories: ['owner/na me'], model: 'opus' })).toMatch(/not an owner\/name/)
    // With a brief the model is not needed: dispatch takes a profile.
    expect(newTaskCommands({ task: 'fix-ci', brief: 'b', repositories: ['o/r'], model: '' }).dispatch[0].ok).toBe(true)
    // A refused task value is refused by the command itself.
    const hostileTask = newTaskCommands({ task: '-x', brief: 'b', repositories: ['o/r'], model: 'opus' })
    expect(hostileTask.dispatch[0].ok).toBe(false)
    expect(newTaskCommands({ task: '-x', brief: '', repositories: ['o/r'], model: 'opus' }).create?.ok).toBe(false)
  })
})


describe('placeholders and the shell (REQ:copy-the-command)', () => {
  /** The exit code of `<shell> -n` (parse only, nothing runs) over the text, or undefined when the shell is absent. */
  function parseExit(shell: string, line: string): number | undefined {
    const result = spawnSync(shell, ['-n', '-c', line], { encoding: 'utf8' })
    if (result.error !== undefined) return undefined
    return result.status ?? 1
  }
  const SHELLS = ['bash', 'zsh', 'dash']
  const present = SHELLS.filter((shell) => parseExit(shell, 'true') === 0)

  const target = { machine: 'vm', ssh: { host: 'h', user: 'u' } }
  const SSH: CommandTarget[] = [{}, { machine: 'vm' }, target]
  /** Every template, rendered with its placeholders (when it has any) and again with benign values. */
  const templates = (to: CommandTarget): { name: string; open: CopyCommand; filled: CopyCommand }[] => [
    { name: 'worktreeList', open: worktreeList('t', to), filled: worktreeList('t', to) },
    { name: 'pullRequestCreate', open: pullRequestCreate('t', undefined, to), filled: pullRequestCreate('t', 'done', to) },
    { name: 'worktreeCleanup', open: worktreeCleanup('t', to), filled: worktreeCleanup('t', to) },
    { name: 'worktreeGc', open: worktreeGc(to), filled: worktreeGc(to) },
    { name: 'pullRequestLand', open: pullRequestLand('o/r', 1, to), filled: pullRequestLand('o/r', 1, to) },
    { name: 'worktreeCreate', open: worktreeCreate('t', ['o/r', 'o/s'], {}, to), filled: worktreeCreate('t', ['o/r', 'o/s'], { model: 'opus', promptFile: 'p.md', base: 'main' }, to) },
    { name: 'worktreeCreate with base', open: worktreeCreate('t', ['o/r'], { base: 'main' }, to), filled: worktreeCreate('t', ['o/r'], { model: 'm', promptFile: 'f', base: 'main' }, to) },
    { name: 'branchList', open: branchList('o/r', 'b', to), filled: branchList('o/r', 'b', to) },
    { name: 'fleetStatus', open: fleetStatus('o/r', to), filled: fleetStatus('o/r', to) },
    { name: 'branchCleanup', open: branchCleanup('o/r', 'b', to), filled: branchCleanup('o/r', 'b', to) },
    { name: 'agentStatus', open: agentStatus('a', to), filled: agentStatus('a', to) },
    { name: 'agentLogs', open: agentLogs('a', to), filled: agentLogs('a', to) },
    { name: 'agentStop', open: agentStop('a', to), filled: agentStop('a', to) },
    { name: 'sessionList', open: sessionList(to), filled: sessionList(to) },
    { name: 'sessionSend', open: sessionSend('s', undefined, to), filled: sessionSend('s', 'hello there', to) },
    { name: 'agentDispatch', open: agentDispatch('o/r', 't', { base: 'main' }, to), filled: agentDispatch('o/r', 't', { profile: 'deep', brief: "do it\nit's fine", base: 'main' }, to) },
    { name: 'agentDispatch no base', open: agentDispatch('o/r', 't', {}, to), filled: agentDispatch('o/r', 't', { profile: 'p', brief: 'b' }, to) },
    { name: 'remotePublish', open: remotePublish(to), filled: remotePublish(to) },
    { name: 'remotePublishDryRun', open: remotePublishDryRun(to), filled: remotePublishDryRun(to) },
    { name: 'remoteStatus', open: remoteStatus(to), filled: remoteStatus(to) },
    { name: 'selfUpdate', open: selfUpdate(to), filled: selfUpdate(to) },
    { name: 'daemonStart', open: daemonStart(to), filled: daemonStart(to) },
    { name: 'remoteEnroll', open: remoteEnroll(undefined, to), filled: remoteEnroll('https://hub.example', to) },
    { name: 'cockpitExport', open: cockpitExport(to), filled: cockpitExport(to) },
    { name: 'newTask create', open: newTaskCommands({ task: 't', brief: '', repositories: ['o/r'], model: 'm' }).create as CopyCommand, filled: worktreeCreate('t', ['o/r'], { model: 'm', promptFile: 'f' }, to) },
    { name: 'newTask dispatch', open: newTaskCommands({ task: 't', brief: 'b', repositories: ['o/r'], model: 'm' }).dispatch[0], filled: agentDispatch('o/r', 't', { profile: 'p', brief: 'b' }, to) },
  ]

  // cockpit-views#ac:copy-command-placeholders-are-syntax-errors
  it('has at least one shell to check against', () => {
    expect(present.length).toBeGreaterThan(0)
  })

  it('makes every template with a placeholder a shell syntax error, and every other one parse', () => {
    let checked = 0
    for (const to of SSH) {
      for (const { name, open, filled } of templates(to)) {
        const needs = open.ok && open.needsEdit
        for (const shell of present) {
          if (open.ok) {
            const code = parseExit(shell, open.text)
            if (needs) expect(code, `${shell} must refuse ${name}: ${open.text}`).not.toBe(0)
            else expect(code, `${shell} must parse ${name}: ${open.text}`).toBe(0)
            checked++
          }
          if (filled.ok) {
            expect(filled.needsEdit, name).toBe(false)
            expect(parseExit(shell, filled.text), `${shell} must parse ${name} once filled: ${filled.text}`).toBe(0)
            checked++
          }
        }
      }
    }
    expect(checked).toBeGreaterThan(100)
  }, 60_000)

  it('refuses a placeholder wherever it stands: first, last, after --flag=, before a word, before another placeholder', () => {
    const values = Object.values(PLACEHOLDERS)
    for (const shell of present) {
      for (const value of values) {
        for (const line of [`wb x ${value}`, `wb x ${value} y`, `wb x --f=${value}`, `wb x --f=${value} --g='h'`, `wb x --f=${value} --g=${value}`, `wb x ${value}\nwb y`, `ssh h wb x --f=${value} z`]) {
          expect(parseExit(shell, line), `${shell}: ${line}`).not.toBe(0)
        }
      }
    }
  }, 60_000)

  // cockpit-views#ac:copy-command-for-an-ssh-machine
  it('builds a command that changes something only for this machine: another machine, with or without an ssh route, gets a refusal', () => {
    const remote: CommandTarget[] = [{ machine: 'vm' }, { machine: 'vm', ssh: { host: 'h', user: 'u' } }, { ssh: { host: 'h' } }]
    for (const to of remote) {
      for (const built of [pullRequestCreate('t', undefined, to), pullRequestLand('o/r', 1, to), agentStop('a', to), sessionSend('s', undefined, to)]) {
        expect(built.ok).toBe(false)
        expect(built.ok ? '' : built.reason).toMatch(/only offered for this machine/)
      }
    }
    expect(pullRequestCreate('t', undefined, { machine: 'vm' })).toEqual({ ok: false, reason: "this changes things, so it is only offered for this machine's own entries: it is vm's, run it there" })
    // Reading commands, and the New task creation, stay available for another machine.
    for (const to of remote) for (const built of [worktreeList('t', to), agentStatus('a', to), agentLogs('a', to), sessionList(to), worktreeCleanup('t', to), worktreeCreate('t', ['o/r'], {}, to)]) expect(built.ok).toBe(true)
    expect(pullRequestLand('o/r', 1, {}).ok).toBe(true)
  })

  it('says an ssh command with a placeholder needs the typed text quoted twice, and that is what makes it one word on the remote', () => {
    const ssh = { ssh: { host: 'h', user: 'u' } }
    const open = worktreeCreate('t', ['o/r'], {}, ssh)
    expect(open).toMatchObject({ ok: true, needsEdit: true, quoteTwice: true })
    // A command with nothing to edit, or one run here, carries no such hint.
    expect(worktreeCreate('t', ['o/r'], { model: 'm', promptFile: 'f' }, ssh)).not.toHaveProperty('quoteTwice')
    expect(worktreeCreate('t', ['o/r'], {}, {})).not.toHaveProperty('quoteTwice')
    if (!open.ok) return
    // `ssh` joins its arguments with spaces and the remote shell splits them again; `wb` prints what it was given.
    const remote = (line: string): string[] | undefined => {
      const script = ['ssh() { shift; eval "$*"; }', "wb() { printf '%s\\n' \"$@\"; }", line].join('\n')
      const result = spawnSync('bash', ['-c', script], { encoding: 'utf8' })
      return result.error === undefined ? result.stdout.split('\n').slice(0, -1) : undefined
    }
    const typed = (replacement: string): string[] | undefined => remote(open.text.replace(PLACEHOLDERS.model, replacement).replace(PLACEHOLDERS.promptFile, "'f'"))
    if (remote('wb a') === undefined) return
    // Typed once-quoted (what the hint warns about) the remote shell splits it in two; quoted twice it stays one.
    expect(typed("'two words'")).toContain('--model=two')
    expect(typed("''\\''two words'\\'''")).toContain('--model=two words')
  })

  it('refuses the invisible look-alike characters of the review, in a value and in a text', () => {
    for (const char of [' ', '­', ' ', ' ', '⁠', '⁤', '　', 'ᅟ', 'ㅤ', '️', '\u{e0100}', '\u{e0041}']) {
      expect(valueProblem(`a${char}b`), char.codePointAt(0)?.toString(16)).toMatch(/control character/)
      expect(valueProblem(`a${char}b`, true), char.codePointAt(0)?.toString(16)).toMatch(/control character/)
    }
    expect(valueProblem('a b')).toBeUndefined()
    expect(valueProblem('a\tb\nc', true)).toBeUndefined()
  })

  it('keeps PLACEHOLDERS in the <<<edit:name>>> form the UI marks', () => {
    for (const value of Object.values(PLACEHOLDERS)) expect(value).toMatch(/^<<<edit:[a-z-]+>>>$/)
  })
})
