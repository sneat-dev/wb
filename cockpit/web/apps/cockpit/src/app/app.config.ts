import {
  ApplicationConfig,
  provideBrowserGlobalErrorListeners,
  provideZonelessChangeDetection,
} from '@angular/core'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { appRoutes } from './app.routes'
import { providePageTitle } from './shell/page-title'
import { Shortcuts } from './shortcuts/shortcuts'

export function createAppConfig(): ApplicationConfig {
  return {
    providers: [
      provideBrowserGlobalErrorListeners(),
      provideZonelessChangeDetection(),
      provideRouter(appRoutes, withComponentInputBinding()),
      providePageTitle(),
      // Every list answers the shell's `/` and Esc.
      { provide: LIST_SHORTCUTS, useExisting: Shortcuts },
    ],
  }
}
