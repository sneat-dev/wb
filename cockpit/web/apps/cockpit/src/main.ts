import { bootstrapApplication } from '@angular/platform-browser'
import { takeLoginKey } from '@cockpit/fleet-data'
import { createAppConfig } from './app/app.config'
import { App } from './app/app'

// The login URL's fragment holds the session key (cockpit#req:session-key): it is taken out of the address
// before the router, or anything else, reads the address.
bootstrapApplication(App, createAppConfig(takeLoginKey(window))).catch((err) =>
  console.error(err),
)
