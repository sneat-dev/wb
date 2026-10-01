import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'

/** A circle as a path, so every icon is a list of `d` strings. */
function circle(cx: number, cy: number, r: number): string {
  return `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`
}

/**
 * The icons of the Cockpit: 24 px outlines drawn with a 1.75 px stroke, in the
 * style of Lucide. They are inline SVG because the content security policy
 * allows no data: image. Add a name here before using it in a template.
 */
const ICONS = {
  search: [circle(11, 11, 7), 'm20 20-3.5-3.5'],
  plus: ['M12 5v14M5 12h14'],
  check: ['m5 12.5 4.5 4.5L19 7.5'],
  'check-circle': [circle(12, 12, 9), 'm8 12.5 2.8 2.8L16 9.5'],
  'x-circle': [circle(12, 12, 9), 'm9 9 6 6m0-6-6 6'],
  'minus-circle': [circle(12, 12, 9), 'M8.5 12h7'],
  alert: ['M12 4 3 19.5h18L12 4Z', 'M12 10v4.5M12 17.2v.1'],
  clock: [circle(12, 12, 9), 'M12 7v5l3 2'],
  x: ['M6 6l12 12M18 6 6 18'],
  keyboard: ['M3 7.5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-9Z', 'M7 10h.1M10 10h.1M13 10h.1M16 10h.1M8 14.5h8'],
  'corner-down-left': ['m9 10-5 5 5 5', 'M20 4v7a4 4 0 0 1-4 4H4'],
  'list-checks': ['M10 6h10M10 12h10M10 18h10', 'm3.5 6 1.2 1.2L7 5M3.5 12l1.2 1.2L7 11M3.5 18l1.2 1.2L7 17'],
  folder: ['M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z'],
  layers: ['m12 3 9 5-9 5-9-5 9-5Z', 'm3 13 9 5 9-5'],
  'git-branch': [circle(6, 5.5, 2), circle(6, 18.5, 2), circle(18, 8.5, 2), 'M6 7.5v9M18 10.5c0 4.5-12 2.5-12 6'],
  bot: ['M5 9a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V9Z', 'M12 3.5V7M9 13v.1M15 13v.1M9.5 16.5h5'],
  server: ['M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 9.5v-4Z', 'M4 14.5A1.5 1.5 0 0 1 5.5 13h13a1.5 1.5 0 0 1 1.5 1.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 18.5v-4Z', 'M8 7.5h.1M8 16.5h.1'],
  user: [circle(12, 7.5, 4), 'M20 21v-1a5 5 0 0 0-5-5H9a5 5 0 0 0-5 5v1'],
  history: ['M3 12a9 9 0 1 0 3-6.7L3 8', 'M3 3v5h5', 'M12 7.5V12l3 2'],
  refresh: ['M20 12a8 8 0 0 1-14 5.3L4 15', 'M4 20v-5h5', 'M4 12a8 8 0 0 1 14-5.3L20 9', 'M20 4v5h-5'],
} as const satisfies Record<string, readonly string[]>

export type IconName = keyof typeof ICONS

/** An inline icon, 16 px by default, drawn in the current text colour and hidden from assistive technology. */
@Component({
  selector: 'app-icon',
  template: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    @for (d of paths(); track $index) {
      <path [attr.d]="d" />
    }
  </svg>`,
  styles: `
    :host {
      display: inline-flex;
      flex: none;
      width: var(--icon-size, 1rem);
      height: var(--icon-size, 1rem);
    }
    svg {
      width: 100%;
      height: 100%;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Icon {
  readonly name = input.required<IconName>()
  protected readonly paths = computed(() => ICONS[this.name()])
}
