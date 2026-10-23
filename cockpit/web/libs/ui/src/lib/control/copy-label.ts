import { CopyCommand } from '@cockpit/fleet-data'

/** The word on a copy button: "Copy", or "Copy template" for a command with a part to edit. Nothing else is ever said. */
export function copyWord(needsEdit: boolean): 'Copy' | 'Copy template' {
  return needsEdit ? 'Copy template' : 'Copy'
}

const VERB_WORD = /^[a-z][a-z-]*$/

/**
 * The `wb` verb a command text runs, as words: "wb pr land" for `wb pr land 'o/r#1'`, and for the ssh form the
 * verb after the destination and the executable. A text with no `wb` in it gives its first word.
 */
export function commandVerb(text: string): string {
  const words = text.split(' ')
  const at = words.findIndex((word, index) => word === 'wb' || (index > 0 && words[0] === 'ssh' && /(^|\/)wb'*$/.test(word)))
  if (at < 0) return words[0]
  const verb = ['wb']
  for (const word of words.slice(at + 1, at + 4)) {
    if (!VERB_WORD.test(word)) break
    verb.push(word)
  }
  return verb.join(' ')
}

/**
 * The accessible name of a copy button: its visible word first (so the name contains what is shown), then the verb
 * it copies, then what it is for: "Copy wb pr land: Land sneat-dev/wb#12". A button whose command is built when it is
 * pressed gives the verb it will build.
 */
export function copyLabel(needsEdit: boolean, verb: string, subject?: string): string {
  return `${copyWord(needsEdit)} ${verb}${subject === undefined ? '' : `: ${subject}`}`
}

/** `copyLabel` for a command text. */
export function copyName(needsEdit: boolean, text: string, subject?: string): string {
  return copyLabel(needsEdit, commandVerb(text), subject)
}

/** `copyName` for a command the library built; a refused command has nothing to copy, and the name says so. */
export function copyNameOf(command: CopyCommand, subject?: string): string {
  return command.ok ? copyName(command.needsEdit, command.text, subject) : `Not copyable${subject === undefined ? '' : `: ${subject}`}`
}
