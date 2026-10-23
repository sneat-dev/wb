import { commandVerb, copyLabel, copyName, copyNameOf, copyWord } from './copy-label'

describe('copy button words and names', () => {
  // cockpit-views#ac:copy-vocabulary
  it('says "Copy" for a command and "Copy template" for one with a part to edit, and nothing else', () => {
    expect(copyWord(false)).toBe('Copy')
    expect(copyWord(true)).toBe('Copy template')
  })

  it('finds the wb verb of a command text, also behind ssh, and stops at the first value or flag', () => {
    expect(commandVerb("wb pr land 'sneat-dev/wb#12'")).toBe('wb pr land')
    expect(commandVerb('wb worktree gc')).toBe('wb worktree gc')
    expect(commandVerb('wb self-update')).toBe('wb self-update')
    expect(commandVerb("wb fleet status --filter='o/r'")).toBe('wb fleet status')
    expect(commandVerb('wb remote publish --dry-run')).toBe('wb remote publish')
    expect(commandVerb("ssh alex@vm.example /usr/local/bin/wb session send 's-1' --message=<<<edit:message>>>")).toBe('wb session send')
    expect(commandVerb("ssh u@h wb worktree list 'x'")).toBe('wb worktree list')
    // What is not a wb command gives its first word.
    expect(commandVerb('ls -l')).toBe('ls')
    expect(commandVerb("ssh h ''\\''/opt/my wb'\\''' worktree list")).toBe('ssh')
  })

  it('names a button with its word first, then the verb, then what it is for', () => {
    expect(copyLabel(false, 'wb pr land', 'o/r#1')).toBe('Copy wb pr land: o/r#1')
    expect(copyLabel(true, 'wb pr create')).toBe('Copy template wb pr create')
    expect(copyName(false, "wb agent logs 'run-1'", 'Logs')).toBe('Copy wb agent logs: Logs')
    expect(copyName(true, "wb pr create 'x' --message=<<<edit:message>>>")).toBe('Copy template wb pr create')
    expect(copyNameOf({ ok: true, text: 'wb remote status', needsEdit: false }, 'Remote status')).toBe('Copy wb remote status: Remote status')
    expect(copyNameOf({ ok: false, reason: 'no' }, 'Land')).toBe('Not copyable: Land')
    expect(copyNameOf({ ok: false, reason: 'no' })).toBe('Not copyable')
  })
})
