// Judges the content security policy violations the browser reported while the
// end-to-end test loaded the shell.
//
// PrimeNG 22 shows an "Invalid PrimeUI license" banner when no PrimeUI license
// is configured. The banner lives in a closed shadow root under #p-license-host
// and is built with inline style attributes, which the strict policy blocks by
// design. Exactly the violations reported for that host are set aside; the same
// directive from any other element is a real violation. Once a license is
// configured the banner is gone and nothing is set aside.
//
// Each violation is { directive, blockedURI, inLicenseBanner }; the page script
// sets inLicenseBanner from the event's composed path.

export interface Violation {
  directive: string
  blockedURI: string
  inLicenseBanner: boolean
}

export function unexplainedViolations(violations: Violation[]): string[] {
  return violations
    .filter((violation) => !violation.inLicenseBanner)
    .map((violation) => `CSP violation: ${violation.directive} ${violation.blockedURI}`)
}

// The browser also logs each violation as a console error. Those are already
// judged through the violation list, so they are not counted a second time;
// every other console error is.
export function otherConsoleErrors(messages: string[]): string[] {
  return messages.filter((message) => !message.includes('violates the following Content Security Policy'))
}
