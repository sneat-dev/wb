import { bootstrapApplication } from '@angular/platform-browser'
import { createAppConfig } from './app/app.config'
import { App } from './app/app'

bootstrapApplication(App, createAppConfig(document)).catch((err) =>
  console.error(err),
)
