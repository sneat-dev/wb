// Judges the content security policy violations the browser reported while the
// end-to-end test loaded the shell.
//
// Every violation reported is a failure: nothing is set aside.

export interface Violation {
  directive: string
  blockedURI: string
}

export function unexplainedViolations(violations: Violation[]): string[] {
  return violations.map((violation) => `CSP violation: ${violation.directive} ${violation.blockedURI}`)
}

// The browser also logs each violation as a console error. Those are already
// judged through the violation list, so they are not counted a second time;
// every other console error is.
export function otherConsoleErrors(messages: string[]): string[] {
  return messages.filter((message) => !message.includes('violates the following Content Security Policy'))
}
