import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core'
import { CopyCommand, PLACEHOLDERS, PanelCommand } from '@cockpit/fleet-data'
import { CopyButton } from './copy-button'
import { copyNameOf, copyWord } from './copy-label'
import { Glyph } from './glyph'
import { GLYPH_BAN, GLYPH_PENCIL, GLYPH_TERMINAL } from './glyphs'

/** A piece of a command's text: plain, or a placeholder the operator must replace. */
export interface CommandSegment {
  text: string
  placeholder: boolean
}

const escapeRegExp = (text: string): string => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// The library's own placeholder values (`<message>`, `<model>`, ...), exactly: nothing else in a command is marked.
const PLACEHOLDER = new RegExp(`(${Object.values(PLACEHOLDERS).map(escapeRegExp).join('|')})`)

/**
 * The same text as `command.text`, cut at the library's placeholder values so
 * those can be marked. Joining the segments gives the text back exactly. A
 * `<word>` that the library did not write is not a placeholder: it is part of
 * a quoted value and stays plain.
 */
export function commandSegments(text: string): CommandSegment[] {
  return text.split(PLACEHOLDER).map((part, index) => ({ text: part, placeholder: index % 2 === 1 })).filter((segment) => segment.text !== '')
}

/** Where a command runs: "run here" unless the library labelled it ("run on <machine>"). */
export function runLocation(command: Extract<CopyCommand, { ok: true }>): string {
  return command.label ?? 'run here'
}

/** What an ssh command with a placeholder needs said beside it: the remote shell splits the arguments again. */
export const QUOTE_TWICE_HINT = 'Quote twice: the remote shell splits this again'

interface Row {
  title: string
  command: CopyCommand
  segments: CommandSegment[]
}

/**
 * The "Copy command" list of an entity (REQ:copy-the-command): for each entry its
 * title, the command in monospace on one line, a copy button, where it runs
 * ("run here" or "run on <machine>") and, when the text holds a placeholder, an
 * "edit before running" mark. A command the library refused to build shows the
 * reason and no button. The component prints only the text of commands the
 * library produced; it builds none. Nothing here runs a command.
 */
@Component({
  selector: 'app-copy-command-list',
  imports: [CopyButton, Glyph],
  templateUrl: './copy-command-list.html',
  styleUrl: './copy-command-list.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CopyCommandList {
  readonly entries = input.required<readonly PanelCommand[]>()
  /** Emits the text that was copied. */
  readonly copied = output<string>()

  protected readonly rows = computed<Row[]>(() =>
    this.entries().map((entry) => ({ title: entry.title, command: entry.command, segments: entry.command.ok ? commandSegments(entry.command.text) : [] })),
  )
  protected readonly where = runLocation
  protected readonly label = (row: Row): string => copyNameOf(row.command, row.title)
  protected readonly word = (row: Row): 'Copy' | 'Copy template' => copyWord(row.command.ok && row.command.needsEdit)
  protected readonly hint = (command: Extract<CopyCommand, { ok: true }>): string | undefined => (command.quoteTwice ? QUOTE_TWICE_HINT : undefined)
  protected readonly terminal = GLYPH_TERMINAL
  protected readonly pencil = GLYPH_PENCIL
  protected readonly ban = GLYPH_BAN
  protected readonly doneStatus = 'Copied; edit the <…> parts before running'
}
