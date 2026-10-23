import { Directive, TemplateRef, inject, input } from '@angular/core'

/**
 * The template of one column's cell: `<ng-template appCell="state" let-item>`.
 * The context's `$implicit` is the row's item; `row` is the whole list row.
 */
@Directive({ selector: 'ng-template[appCell]' })
export class ListCell {
  readonly id = input.required<string>({ alias: 'appCell' })
  readonly template = inject(TemplateRef)
}

/** The template of the side panel of the selected row: `<ng-template appListPanel let-item>`. */
@Directive({ selector: 'ng-template[appListPanel]' })
export class ListPanelTemplate {
  readonly template = inject(TemplateRef)
}
