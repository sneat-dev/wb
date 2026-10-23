package githubchecks

func RunIncludesPullRequest(run ActionsRun, number int, base string) bool {
	for _, pr := range run.PullRequests {
		if pr.Number == number && pr.Base.Ref == base {
			return true
		}
	}
	return false
}
