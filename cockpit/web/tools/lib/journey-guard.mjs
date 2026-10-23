// The whole-journey test starts a real wb daemon, so it runs only where that is
// harmless: a disposable GitHub Actions Linux runner. On macOS wb supervises its
// daemon through one launchd job with a fixed label per user, in the user's own
// launchd domain, so an "isolated" start there replaces the machine's real
// daemon. Linux alone is not enough either: a developer's Linux box may run a
// real daemon, so the test also requires GitHub Actions.

export function assertJourneyHost(platform, env) {
  if (platform !== 'linux') {
    throw new Error(`journey: refusing to run on ${platform}: the daemon cannot be isolated from the machine's own launchd job; run it on Linux in CI`)
  }
  if (env.GITHUB_ACTIONS !== 'true') {
    throw new Error('journey: refusing to run outside GitHub Actions: it starts a real daemon and belongs on a disposable runner')
  }
}
