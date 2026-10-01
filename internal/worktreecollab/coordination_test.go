package worktreecollab

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func testCheckout() Checkout {
	return Checkout{ID: "checkout-1", Root: "/root/worktree", GitDir: "/repo/.git/worktrees/one", CommonDir: "/repo/.git"}
}

func testState(t *testing.T) State {
	t.Helper()
	state, err := New(testCheckout())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestCheckoutAndStateValidation(t *testing.T) {
	t.Parallel()
	for _, checkout := range []Checkout{{}, {ID: "bad/id", Root: "r", GitDir: "g", CommonDir: "c"}, {ID: "bad:name", Root: "r", GitDir: "g", CommonDir: "c"}, {ID: "good", Root: "", GitDir: "g", CommonDir: "c"}} {
		if _, err := New(checkout); err == nil {
			t.Fatalf("New(%+v) accepted invalid checkout", checkout)
		}
	}
	state := testState(t)
	if err := state.Validate(testCheckout()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*State){
		func(s *State) { s.Version++ },
		func(s *State) { s.Owner = "missing" },
		func(s *State) { s.Members = nil },
		func(s *State) { s.Inboxes = nil },
		func(s *State) { s.Requests = nil },
		func(s *State) { s.Cursors = nil },
		func(s *State) { s.Notices = nil },
	} {
		copy := testState(t)
		change(&copy)
		if err := copy.Validate(testCheckout()); err == nil {
			t.Fatal("corrupt state validated")
		}
	}
	if err := state.Validate(Checkout{ID: "another", Root: "r", GitDir: "g", CommonDir: "c"}); err == nil {
		t.Fatal("checkout rebinding validated")
	}
}

func TestOwnerAndMembershipTransitions(t *testing.T) {
	t.Parallel()
	state, _ := New(testCheckout())
	if err := state.Join("peer", testNow); err == nil {
		t.Fatal("join initialized ownership")
	}
	if err := state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow}); err != nil {
		t.Fatal(err)
	}
	if state.Owner != "owner" || len(state.Members) != 1 || state.OwnerEpoch != 1 {
		t.Fatalf("initial take = %+v", state)
	}
	if err := state.Take(TakeRequest{Caller: "owner", ExpectedOwner: "owner", At: testNow}); err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 {
		t.Fatalf("idempotent take advanced revision %d", state.Revision)
	}
	if err := state.Take(TakeRequest{Caller: "other", ExpectedOwner: NoOwner, Force: true, Reason: "take", At: testNow}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected owner: %v", err)
	}
	if err := state.Join("peer", testNow); err != nil {
		t.Fatal(err)
	}
	if err := state.Join("peer", testNow); err != nil {
		t.Fatal(err)
	}
	if err := state.Leave("owner"); err == nil {
		t.Fatal("owner left")
	}
	if err := state.Transfer("other", "peer", true, testNow); err == nil {
		t.Fatal("non-owner transferred")
	}
	if err := state.Transfer("owner", "peer", false, testNow); err == nil {
		t.Fatal("dead peer received ownership")
	}
	if err := state.Transfer("owner", "missing", true, testNow); err == nil {
		t.Fatal("unjoined peer received ownership")
	}
	if err := state.Transfer("owner", "peer", true, testNow); err != nil {
		t.Fatal(err)
	}
	if state.Owner != "peer" || state.OwnerEpoch != 2 || state.Notices["owner"].Current != "peer" {
		t.Fatalf("transfer did not notify members: %+v", state)
	}
	if err := state.Transfer("peer", "peer", true, testNow); err != nil {
		t.Fatal(err)
	}
	if err := state.Leave("owner"); err != nil {
		t.Fatal(err)
	}
	if err := state.Leave("owner"); err != nil {
		t.Fatal(err)
	}
}

func TestTakeLegacyExpectedOwnerAndLiveness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		req       TakeRequest
		wantError string
	}{
		{"missing expectation", TakeRequest{Caller: "new", At: testNow}, "requires"},
		{"unresolved legacy observation", TakeRequest{Caller: "new", ExpectedOwner: NoOwner, Legacy: ObservedOwner{Status: "unknown"}, Force: true, Reason: "take", At: testNow}, "unresolved"},
		{"none cannot override legacy", TakeRequest{Caller: "new", ExpectedOwner: NoOwner, Legacy: ObservedOwner{ID: "legacy", Status: "unknown"}, Force: true, Reason: "take", At: testNow}, "expected owner"},
		{"live refuses without force", TakeRequest{Caller: "new", ExpectedOwner: "legacy", Legacy: ObservedOwner{ID: "legacy", Status: "live"}, At: testNow}, "force"},
		{"force requires reason", TakeRequest{Caller: "new", ExpectedOwner: "legacy", Legacy: ObservedOwner{ID: "legacy", Status: "unknown"}, Force: true, At: testNow}, "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, _ := New(testCheckout())
			if err := state.Take(tc.req); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Take error = %v, want %q", err, tc.wantError)
			}
			if state.Owner != "" || state.Revision != 0 {
				t.Fatalf("failed take mutated %+v", state)
			}
		})
	}
	for _, tc := range []TakeRequest{
		{Caller: "prior", ExpectedOwner: "legacy", Legacy: ObservedOwner{ID: "legacy", SessionID: "prior", Status: "live"}, At: testNow},
		{Caller: "new", ExpectedOwner: "legacy", Legacy: ObservedOwner{ID: "legacy", Status: "inactive"}, At: testNow},
		{Caller: "new", ExpectedOwner: "legacy", Legacy: ObservedOwner{ID: "legacy", Status: "unknown"}, Force: true, Reason: "confirmed", At: testNow},
	} {
		state, _ := New(testCheckout())
		if err := state.Take(tc); err != nil {
			t.Fatal(err)
		}
		if state.Owner != tc.Caller || state.OwnerChanges[0].Previous != "legacy" {
			t.Fatalf("legacy take = %+v", state)
		}
	}
	if err := (&State{}).Take(TakeRequest{Caller: "new", ExpectedOwner: NoOwner, At: testNow}); err == nil {
		t.Fatal("uninitialized state accepted ownership")
	}
}

func TestMessageRetryCursorAndCapacity(t *testing.T) {
	t.Parallel()
	state := testState(t)
	for _, peer := range []string{"a", "b"} {
		if err := state.Join(peer, testNow); err != nil {
			t.Fatal(err)
		}
	}
	first := SendRequest{Sender: "owner", Recipients: []string{"b", "a"}, IdempotencyKey: "key", MessageID: "message-1", Kind: "text", Body: "hello", At: testNow}
	receipt, replay, err := state.Send(first)
	if err != nil || replay || receipt.Digest != digest("hello") || receipt.Recipients[0] != "a" {
		t.Fatalf("send = %+v, %v, %v", receipt, replay, err)
	}
	if err := state.Transfer("owner", "a", true, testNow); err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := state.Send(first)
	if err != nil || !replay || replayed.OwnerEpoch != 1 || len(state.Inboxes["a"]) != 1 {
		t.Fatalf("retry after owner transfer = %+v, %v, %v", replayed, replay, err)
	}
	changed := first
	changed.Body = "different"
	if _, _, err := state.Send(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed bytes accepted: %v", err)
	}
	changed = first
	changed.Sender = "a"
	if _, _, err := state.Send(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("different joined sender reused key: %v", err)
	}
	changed = first
	changed.IdempotencyKey = "second-key"
	if _, _, err := state.Send(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate message ID accepted: %v", err)
	}
	second := first
	second.IdempotencyKey, second.MessageID, second.Body = "two", "message-2", "second"
	if _, _, err := state.Send(second); err != nil {
		t.Fatal(err)
	}
	messages, err := state.Inbox("a")
	if err != nil || len(messages) != 2 {
		t.Fatalf("inbox = %+v, %v", messages, err)
	}
	if cursor, err := state.Ack("a", second.MessageID, digest(second.Body), testNow); err != nil || cursor != 0 {
		t.Fatalf("out-of-order ack = %d, %v", cursor, err)
	}
	if cursor, err := state.Ack("a", first.MessageID, digest(first.Body), testNow); err != nil || cursor != 2 {
		t.Fatalf("cursor did not advance over both messages: %d, %v", cursor, err)
	}
	if cursor, err := state.Ack("a", first.MessageID, digest(first.Body), testNow); err != nil || cursor != 2 {
		t.Fatalf("idempotent ack = %d, %v", cursor, err)
	}
	third := first
	third.IdempotencyKey, third.MessageID, third.Body = "three", "message-3", "third"
	if _, _, err := state.Send(third); err != nil {
		t.Fatal(err)
	}
	if cursor, err := state.Ack("a", third.MessageID, digest(third.Body), testNow); err != nil || cursor != 3 {
		t.Fatalf("cursor did not skip previously consumed messages: %d, %v", cursor, err)
	}
	if _, err := state.Ack("a", "message-2", digest("wrong"), testNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong digest accepted: %v", err)
	}
	if _, err := state.Ack("a", "missing", digest("x"), testNow); err == nil {
		t.Fatal("missing message acknowledged")
	}
	if _, err := state.Inbox("unjoined"); err == nil {
		t.Fatal("unjoined session read inbox")
	}
	if len(state.Notices) != 3 {
		t.Fatalf("owner notices lost: %+v", state.Notices)
	}
}

func TestMessageRefusalsPreserveState(t *testing.T) {
	t.Parallel()
	base := testState(t)
	if err := base.Join("peer", testNow); err != nil {
		t.Fatal(err)
	}
	good := SendRequest{Sender: "owner", Recipients: []string{"peer"}, IdempotencyKey: "key", MessageID: "one", Kind: "text", Body: "body", At: testNow}
	for _, tc := range []struct {
		name   string
		mutate func(*SendRequest)
	}{
		{"empty sender", func(r *SendRequest) { r.Sender = "" }},
		{"unjoined sender", func(r *SendRequest) { r.Sender = "stranger" }},
		{"empty key", func(r *SendRequest) { r.IdempotencyKey = "" }},
		{"empty ID", func(r *SendRequest) { r.MessageID = "" }},
		{"wrong kind", func(r *SendRequest) { r.Kind = "binary" }},
		{"oversize", func(r *SendRequest) { r.Body = strings.Repeat("x", MaxMessageBodyBytes+1) }},
		{"no recipients", func(r *SendRequest) { r.Recipients = nil }},
		{"duplicate recipients", func(r *SendRequest) { r.Recipients = []string{"peer", "peer"} }},
		{"unjoined recipient", func(r *SendRequest) { r.Recipients = []string{"missing"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := testState(t)
			if err := state.Join("peer", testNow); err != nil {
				t.Fatal(err)
			}
			request := good
			tc.mutate(&request)
			if _, _, err := state.Send(request); err == nil || state.Revision != 2 || len(state.Inboxes["peer"]) != 0 {
				t.Fatalf("invalid request = %v, state %+v", err, state)
			}
		})
	}
	if _, _, err := (&State{}).Send(good); err == nil {
		t.Fatal("unjoined sender accepted")
	}
	if err := base.Leave(""); err == nil {
		t.Fatal("invalid leave accepted")
	}
	if _, err := base.Inbox(""); err == nil {
		t.Fatal("invalid inbox recipient accepted")
	}
	if _, err := base.Ack("", "one", digest("body"), testNow); err == nil {
		t.Fatal("invalid acknowledgement accepted")
	}
	if _, err := base.Ack("missing", "one", digest("body"), testNow); err == nil {
		t.Fatal("unjoined recipient acknowledged")
	}
	full := testState(t)
	if err := full.Join("peer", testNow); err != nil {
		t.Fatal(err)
	}
	full.Requests = make(map[string]SendReceipt, MaxStoredMessages)
	for index := 0; index < MaxStoredMessages; index++ {
		full.Requests[string(rune(index+0x4000))] = SendReceipt{}
	}
	if _, _, err := full.Send(good); err == nil || !strings.Contains(err.Error(), "storage is full") {
		t.Fatalf("full storage = %v", err)
	}
	full.Requests = map[string]SendReceipt{}
	full.Inboxes["peer"] = make([]Message, MaxPendingPerPeer)
	if _, _, err := full.Send(good); err == nil || !strings.Contains(err.Error(), "inbox is full") {
		t.Fatalf("full recipient = %v", err)
	}
}
