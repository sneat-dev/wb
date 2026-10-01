import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { PAGE_LINKS } from '../nav'
import { OverlayFocus } from '../shell/overlay-focus'
import { ShellState } from '../shell/shell-state'
import { Icon } from '../ui/icon'
import { modifierLabel } from './platform'

interface ShortcutRow {
  /** Each key is shown on its own key cap; a sequence is keys one after the other. */
  keys: string[]
  label: string
}

interface ShortcutGroup {
  title: string
  rows: ShortcutRow[]
}

/** The shortcut sheet (`?`): every shortcut of REQ:keyboard-shortcuts and the list keys of REQ:row-keyboard-and-copy. */
@Component({
  selector: 'app-shortcut-sheet',
  imports: [OverlayFocus, Icon],
  templateUrl: './shortcut-sheet.html',
  styleUrl: './shortcut-sheet.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ShortcutSheet {
  protected readonly shell = inject(ShellState)
  private readonly modifier = modifierLabel(inject(DOCUMENT).defaultView?.navigator)

  protected readonly groups = computed<ShortcutGroup[]>(() => [
    { title: 'Go to', rows: PAGE_LINKS.map((link) => ({ keys: ['g', link.key], label: link.label })) },
    {
      title: 'Find',
      rows: [
        { keys: ['/'], label: 'Filter this list, or search everything when no list is shown' },
        { keys: [this.modifier, 'K'], label: 'Search everything, from anywhere' },
      ],
    },
    {
      title: 'In a list',
      rows: [
        { keys: ['j'], label: 'Next row' },
        { keys: ['k'], label: 'Previous row' },
        { keys: ['Enter'], label: 'Open the selected row in the side panel' },
      ],
    },
    {
      title: 'General',
      rows: [
        { keys: ['Esc'], label: 'Clear the filter, close the search, the panel or this sheet' },
        { keys: ['?'], label: 'Show this sheet' },
      ],
    },
  ])
}
