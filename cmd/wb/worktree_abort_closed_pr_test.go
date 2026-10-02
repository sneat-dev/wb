package main

import "testing"

func TestClosedAuditSuffixNamesTheRecordOnlyWhenOneExists(t *testing.T) {
	t.Parallel()
	if closedAuditSuffix("") != "" || closedAuditSuffix("/h/closed-pr-discards/x.json") != "; audit /h/closed-pr-discards/x.json" {
		t.Fatal("unexpected audit suffix")
	}
}
