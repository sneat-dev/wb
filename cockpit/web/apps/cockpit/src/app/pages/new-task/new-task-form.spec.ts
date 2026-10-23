import { convertToParamMap } from '@angular/router'
import { FleetModels } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, repository } from '@cockpit/fleet-data/testing'
import { NOW } from '../test-harness'
import { EMPTY_STATE, KNOWN_MODELS, NewTaskState, commandsOf, defaultBranchOf, machineChoices, modelsOffered, nameProblem, queryOf, stateOf } from './new-task-form'
import { OWNER, newTaskDocument } from './new-task-fixture'

const state = (extra: Partial<NewTaskState>): NewTaskState => ({ ...EMPTY_STATE, ...extra })
const texts = (entries: ReturnType<typeof commandsOf>) => entries.map((entry) => (entry.command.ok ? entry.command.text : `refused: ${entry.command.reason}`))

describe('stateOf and queryOf', () => {
  it('reads the repositories, task, base, model, profile and machine of an address, dropping a name that cannot be picked and a repeat', () => {
    expect(stateOf(convertToParamMap({ repo: ['a/b', 'a/b', 'no slash', 'c/d'], task: 'fix', base: 'main', model: 'opus', profile: 'cheap-coder', machine: 'mach-beta' }))).toEqual({
      repositories: ['a/b', 'c/d'],
      task: 'fix',
      base: 'main',
      model: 'opus',
      profile: 'cheap-coder',
      machine: 'mach-beta',
    })
    expect(stateOf(convertToParamMap({}))).toEqual(EMPTY_STATE)
  })

  it('writes only what is filled in, and reads back what it wrote', () => {
    expect(queryOf(EMPTY_STATE)).toEqual({ repo: null, task: null, base: null, model: null, profile: null, machine: null })
    const filled = state({ repositories: ['a/b', 'c/d'], task: 't', base: 'dev', model: 'opus', profile: 'p', machine: 'mach-beta' })
    expect(queryOf(filled)).toEqual({ repo: ['a/b', 'c/d'], task: 't', base: 'dev', model: 'opus', profile: 'p', machine: 'mach-beta' })
    expect(stateOf(convertToParamMap(queryOf(filled) as Record<string, string | string[]>))).toEqual(filled)
  })
})

describe('nameProblem', () => {
  it('accepts a name of the safe set, and says nothing about one not given yet', () => {
    expect([nameProblem(''), nameProblem('fix-ci_2.x')]).toEqual([undefined, undefined])
  })

  it('says why a name cannot be used: the library refuses a leading hyphen and a control character, and the safe set a space', () => {
    expect(nameProblem('-rf')).toContain('starts with "-"')
    expect(nameProblem('a\nb')).toContain('control character')
    expect(nameProblem('a b')).toBe('a task name is made of letters, digits, dots, underscores and hyphens')
    expect(nameProblem('a/b')).toBe('a task name is made of letters, digits, dots, underscores and hyphens')
  })
})

describe('modelsOffered', () => {
  it('lists the known models and then the fleet\'s own, most used first, without repeats', () => {
    const document = fleetDocument({ agents: [agent('a', undefined, 'live', { model: 'zed' }), agent('b', undefined, 'live', { model: 'abc' }), agent('c', undefined, 'live', { model: 'zed' }), agent('d', undefined, 'live', { model: 'opus' }), agent('e', undefined, 'live', { model: undefined })] })
    expect(modelsOffered(document)).toEqual([...KNOWN_MODELS, 'zed', 'abc'])
    expect(modelsOffered(fleetDocument({ agents: [agent('a', undefined, 'live', { model: 'b' }), agent('b', undefined, 'live', { model: 'a' })] })).slice(-2)).toEqual(['a', 'b'])
  })
})

describe('defaultBranchOf', () => {
  const document = newTaskDocument()
  it('is the branch the chosen repositories share, and none when they differ, when one does not say, or when nothing is chosen', () => {
    expect(defaultBranchOf(document, ['sneat-co/sneat-go', 'sneat-co/bots-go'])).toBe('main')
    expect(defaultBranchOf(document, ['sneat-co/sneat-go', 'acme/tools'])).toBeUndefined()
    expect(defaultBranchOf(document, ['acme/odd name'])).toBeUndefined()
    expect(defaultBranchOf(document, [])).toBeUndefined()
    expect(defaultBranchOf(document, ['nobody/here'])).toBeUndefined()
  })
})

describe('commandsOf', () => {
  const filled = state({ repositories: ['sneat-co/sneat-go', 'sneat-co/bots-go'], task: 'fix-ci', base: 'main', model: 'opus', profile: 'cheap-coder' })

  // cockpit-views#ac:new-task-form-produces-commands
  it('is one dispatch command for each repository when a brief is typed, with the profile from the form: dispatch creates the worktree itself, so there is no create before it and no placeholder left', () => {
    const entries = commandsOf(filled, 'Fix the flaky CI.')
    expect(entries.map((entry) => entry.title)).toEqual(['Dispatch an agent in sneat-co/sneat-go', 'Dispatch an agent in sneat-co/bots-go'])
    expect(texts(entries)).toEqual([
      "wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'",
      "wb agent dispatch --repo='sneat-co/bots-go' --task='Fix the flaky CI.' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'",
    ])
    // Nothing is left to edit, and nothing creates the worktree twice.
    expect(entries.every((entry) => entry.command.ok && !entry.command.needsEdit)).toBe(true)
    expect(texts(entries).join('\n')).not.toContain('worktree create')
    expect(texts(entries).join('\n')).not.toContain('--model')
  })

  it('keeps one placeholder, the profile, only when the form has none: the one thing the page cannot know', () => {
    const entries = commandsOf(state({ ...filled, profile: '  ', base: '' }), 'x')
    expect(texts(entries)[0]).toBe("wb agent dispatch --repo='sneat-co/sneat-go' --task='x' --profile=<<<edit:profile>>> --new-worktree='fix-ci'")
    expect(entries.every((entry) => entry.command.ok && entry.command.needsEdit)).toBe(true)
  })

  it('is the creation command alone, with the model from the form, while there is no brief', () => {
    for (const brief of ['', '   \n ']) {
      const entries = commandsOf(filled, brief)
      expect(entries.map((entry) => entry.title)).toEqual(['Create the worktrees'])
      expect(texts(entries)).toEqual(["wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'"])
    }
    expect(texts(commandsOf(state({ ...filled, base: '' }), ''))).toEqual(["wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>>"])
  })

  it('refuses until a repository is chosen and the task is named, and a model is entered when there is no brief, with the reason and no command', () => {
    expect(texts(commandsOf(state({ ...filled, model: ' ' }), ''))).toEqual(['refused: a model is required to create the worktrees (the verb requires --model; "unknown" is its explicit value)'])
    // With a brief the model is not asked for: dispatch takes a profile.
    expect(texts(commandsOf(state({ ...filled, model: '' }), 'b'))[0]).toContain('wb agent dispatch')
    expect(texts(commandsOf(state({ ...filled, repositories: [] }), 'b'))).toEqual(['refused: choose at least one repository'])
    expect(texts(commandsOf(state({ ...filled, task: '' }), 'b'))).toEqual(['refused: name the task: it is the worktree and the branch'])
    expect(texts(commandsOf(state({ ...filled, task: 'a b' }), 'b'))).toEqual(['refused: a task name is made of letters, digits, dots, underscores and hyphens'])
  })

  it("shows the library's refusal of an unsafe base, model, profile or brief", () => {
    expect(texts(commandsOf(state({ ...filled, base: '-x' }), 'b'))[0]).toContain('refused: a value that starts with "-"')
    expect(texts(commandsOf(state({ ...filled, model: 'a\u202eb' }), ''))[0]).toContain('refused: a value with a control character')
    expect(texts(commandsOf(state({ ...filled, profile: '-p' }), 'b'))[0]).toContain('refused: a value that starts with "-"')
    const entries = commandsOf(filled, 'a\u0007b')
    expect(texts(entries)[0]).toContain('refused: a value with a control character')
  })

  it('quotes a brief with quotes and line breaks as one word', () => {
    const [dispatch] = commandsOf(state({ ...filled, repositories: ['sneat-co/sneat-go'] }), "it's\nmulti")
    expect(dispatch.command.ok && dispatch.command.text).toContain("--task='it'\\''s\nmulti'")
  })

  it('writes the same commands through the SSH route of another machine, and refuses the same way', () => {
    const target = { machine: 'beta', ssh: { host: 'beta.example', user: 'me' } }
    // A value the remote shell would split again is quoted twice, as the library does for every ssh command.
    expect(texts(commandsOf(filled, 'do it', target))).toEqual([
      "ssh me@beta.example wb agent dispatch --repo='sneat-co/sneat-go' --task=''\\''do it'\\''' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'",
      "ssh me@beta.example wb agent dispatch --repo='sneat-co/bots-go' --task=''\\''do it'\\''' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'",
    ])
    expect(texts(commandsOf(filled, '', target))).toEqual(["ssh me@beta.example wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'"])
    expect(commandsOf(filled, '', target)[0].command).toMatchObject({ quoteTwice: true })
    expect(texts(commandsOf(state({ ...filled, model: '' }), '', target))).toHaveLength(1)
  })

  it('labels a command of a machine named without a route "run on" it', () => {
    const [dispatch] = commandsOf(filled, 'x', { machine: 'beta' })
    expect(dispatch.command.ok && dispatch.command.label).toBe('run on beta')
  })
})

describe('machineChoices', () => {
  const models = new FleetModels()
  it('offers this machine only to an anonymous reader, however many machines the fleet has', () => {
    const choices = machineChoices(models.forDocument(newTaskDocument(), NOW))
    expect(choices).toEqual([{ id: undefined, name: 'This machine (alpha)', metricsId: 'mach-alpha', target: {} }])
  })

  it('offers each machine with an SSH route to an owner session, with that route as its target', () => {
    const choices = machineChoices(models.forDocument(newTaskDocument(), NOW, OWNER.machine_routes))
    expect(choices.map((choice) => choice.name)).toEqual(['This machine (alpha)', 'beta'])
    expect(choices[1]).toEqual({ id: 'mach-beta', name: 'beta', metricsId: 'mach-beta', target: { machine: 'beta', ssh: { host: 'beta.example', user: 'me', wbPath: undefined } } })
  })

  it('names this machine plainly when the document does not list it, and has no metrics for it', () => {
    const choices = machineChoices(models.forDocument(fleetDocument({ machines: [machine('beta', 'cached')], repositories: [repository('r', 'beta')] }), NOW))
    expect(choices).toEqual([{ id: undefined, name: 'This machine', metricsId: undefined, target: {} }])
  })
})
