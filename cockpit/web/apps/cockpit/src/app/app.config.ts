import {
  ApplicationConfig,
  provideBrowserGlobalErrorListeners,
  provideZonelessChangeDetection,
} from '@angular/core'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { LOGIN_KEY } from '@cockpit/fleet-data'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { appRoutes } from './app.routes'
import { providePageTitle } from './shell/page-title'
import { Shortcuts } from './shortcuts/shortcuts'

/** `loginKey` is the session key the login URL offered this page load (`takeLoginKey`), or null. */
export function createAppConfig(loginKey: string | null = null): ApplicationConfig {
  return {
    providers: [
      provideBrowserGlobalErrorListeners(),
      provideZonelessChangeDetection(),
      provideRouter(appRoutes, withComponentInputBinding()),
      providePageTitle(),
      // Every list answers the shell's `/` and Esc.
      { provide: LIST_SHORTCUTS, useExisting: Shortcuts },
      { provide: LOGIN_KEY, useValue: loginKey },
    ],
  }
}
