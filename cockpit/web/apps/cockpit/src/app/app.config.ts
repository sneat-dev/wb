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

// PrimeNG is provided by the lazy route group (pages/prime-theme.ts), not here:
// it is not part of the initial script.
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
