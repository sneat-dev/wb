package fleet

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
)

// These tests are the cadence of the background reads of other machines
// (cockpit-views#req:remote-exporter-transports): it is the scheduler's, the
// same for every transport, and driven by who is looking. They run over the SSH
// exporter on a fake runner, where one export is one connection.

// reader is who reads the routes in a test: a function that makes one GET.
type reader func(target string)

// The readers: an owner session, an anonymous client on this machine, the hosted
// page (which reads the same metadata routes from another origin) and the export
// verb, which is another machine's daemon.
func (c *cockpitServer) asOwner() reader {
	cookie := c.owner()
	return func(target string) { c.get(target, cookie) }
}

func (c *cockpitServer) asAnonymous() reader {
	return func(target string) { c.get(target, nil) }
}

func (c *cockpitServer) asHostedPage() reader {
	return func(target string) {
		c.t.Helper()
		if recorder := c.get(target, nil, "Origin", hostedOrigin); recorder.Code != http.StatusOK || recorder.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
			c.t.Fatalf("the hosted page's read of %s = %d (the test would be vacuous)", target, recorder.Code)
		}
	}
}

func (c *cockpitServer) asExportVerb() reader {
	return func(target string) { c.get(target, c.owner(), ExportReaderHeader, "1") }
}

// watching runs the daemon's loop for the given time on the fake clock, one
// look every remoteStep, with a reader that reads the fleet document once a
// minute while viewing and a machine's metrics every 10 seconds while onMachines,
// through the real routes.
func watching(t *testing.T, snapshotter *Snapshotter, clock *manualClock, read reader, length time.Duration, viewing, onMachines bool) {
	t.Helper()
	for elapsed := time.Duration(0); elapsed < length; elapsed += remoteStep {
		if viewing && elapsed%time.Minute == 0 {
			read(cockpit.APIPrefix + FleetRoute)
		}
		if onMachines && elapsed%(10*time.Second) == 0 {
			if vm, found := machineNamed(snapshotter.Document(), vmKey); found {
				read(metricsURL + vm.ID)
			}
		}
		pollIdle(t, snapshotter)
		clock.advance(remoteStep)
	}
}

// inTheHourFrom counts the calls made from the given second up to, not including, the
// second an hour later.
func inTheHourFrom(seconds []int, from int) int {
	count := 0
	for _, second := range seconds {
		if second >= from && second < from+3600 {
			count++
		}
	}
	return count
}

// TestOtherMachinesAreReadAsOftenAsSomeoneIsLooking measures SSH logins per
// hour at the default 60 second refresh interval: 4 with nobody looking (one
// keepalive every 15 minutes), 60 while an owner session reads the fleet
// document, 120 while the owner's Machines page also polls the machine's
// metrics, and 1 for a machine whose SSH login is refused, looked at or not.
// Only an owner is demand for an SSH login: an anonymous reader on this machine,
// the hosted page and the export verb leave the machine on its keepalive,
// whatever they read and however often.
func TestOtherMachinesAreReadAsOftenAsSomeoneIsLooking(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	refused := failingSSH(255, "", "alex@vm.example: Permission denied (publickey).")
	for name, test := range map[string]struct {
		answer              func([]string) sshAnswer
		read                func(*cockpitServer) reader
		viewing, onMachines bool
		hours               int
		want                int
	}{
		"nobody looking":                          {answer: exporting(t, full, only), hours: 2, want: 4},
		"an owner reading the fleet":              {answer: exporting(t, full, only), read: (*cockpitServer).asOwner, viewing: true, hours: 2, want: 60},
		"an owner's Machines page open":           {answer: exporting(t, full, only), read: (*cockpitServer).asOwner, viewing: true, onMachines: true, hours: 2, want: 120},
		"an anonymous client reading the fleet":   {answer: exporting(t, full, only), read: (*cockpitServer).asAnonymous, viewing: true, hours: 2, want: 4},
		"an anonymous client's Machines page":     {answer: exporting(t, full, only), read: (*cockpitServer).asAnonymous, viewing: true, onMachines: true, hours: 2, want: 4},
		"the hosted page, with its Machines page": {answer: exporting(t, full, only), read: (*cockpitServer).asHostedPage, viewing: true, onMachines: true, hours: 2, want: 4},
		"the export verb, with an owner's cookie": {answer: exporting(t, full, only), read: (*cockpitServer).asExportVerb, viewing: true, onMachines: true, hours: 2, want: 4},
		"a refused login, looked at":              {answer: refused, read: (*cockpitServer).asOwner, viewing: true, onMachines: true, hours: 3, want: 1},
		"a refused login, an anonymous client":    {answer: refused, read: (*cockpitServer).asAnonymous, viewing: true, onMachines: true, hours: 3, want: 1},
		"a refused login, nobody looking":         {answer: refused, hours: 3, want: 1},
	} {
		runner := &fakeSSH{answer: test.answer}
		snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, nil)
		refreshAndSettle(t, snapshotter)
		server := newCockpitServer(t, snapshotter)
		var read reader
		if test.read != nil {
			read = test.read(server)
		}
		watching(t, snapshotter, clock, read, time.Duration(test.hours)*time.Hour, test.viewing, test.onMachines)
		if got := inTheHourFrom(runner.seconds(), (test.hours-1)*3600); got != test.want {
			t.Errorf("%s: %d connections in the last hour, want %d (at %v)", name, got, test.want, runner.seconds())
		}
		// The machine nobody may log in to for stays shown, with the age of its entries.
		if vm, found := machineNamed(snapshotter.Document(), vmKey); test.want == 4 && (!found || vm.Route != RouteLiveRemote || vm.RemoteError != "") {
			t.Errorf("%s: the machine on its keepalive = %+v", name, vm)
		}
	}
}

// TestAnAnonymousReaderRaisesTheHTTPTransportOnly measures, over a simulated
// hour at the default 60 second interval, a machine that has both routes: an
// anonymous reader on this machine has it read over HTTP as often as an owner
// would (60 times, 120 with its metrics), and causes no SSH login at all while
// HTTP answers. While HTTP fails, the anonymous reader's HTTP reads go on at
// their backoff (which each keepalive SSH answers starts again: 20 requests in
// the hour, none of them a login) and SSH stays on its keepalive (4 logins),
// which keeps the machine shown, with the HTTP failure; an owner gets the
// fallback at once, and the hosted page raises neither transport.
func TestAnAnonymousReaderRaisesTheHTTPTransportOnly(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	down := failing(&RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true})
	for name, test := range map[string]struct {
		http               func(RemoteTarget, bool) (Envelope, error)
		read               func(*cockpitServer) reader
		onMachines         bool
		wantHTTP, wantSSH  int
		transport, failure string
	}{
		"anonymous, http answers":              {http: answering(full, only), read: (*cockpitServer).asAnonymous, wantHTTP: 60, wantSSH: 0, transport: TransportHTTP},
		"anonymous with metrics, http answers": {http: answering(full, only), read: (*cockpitServer).asAnonymous, onMachines: true, wantHTTP: 120, wantSSH: 0, transport: TransportHTTP},
		"anonymous with metrics, http is down": {http: down, read: (*cockpitServer).asAnonymous, onMachines: true, wantHTTP: 20, wantSSH: 4, transport: TransportSSH, failure: RemoteErrorHTTPUnavailable},
		"the hosted page, http is down":        {http: down, read: (*cockpitServer).asHostedPage, onMachines: true, wantHTTP: 4, wantSSH: 4, transport: TransportSSH, failure: RemoteErrorHTTPUnavailable},
		"the hosted page, http answers":        {http: answering(full, only), read: (*cockpitServer).asHostedPage, onMachines: true, wantHTTP: 4, wantSSH: 0, transport: TransportHTTP},
		"an owner with metrics, http is down":  {http: down, read: (*cockpitServer).asOwner, onMachines: true, wantHTTP: 12, wantSSH: 120, transport: TransportSSH, failure: RemoteErrorHTTPUnavailable},
		"an owner with metrics, http answers":  {http: answering(full, only), read: (*cockpitServer).asOwner, onMachines: true, wantHTTP: 120, wantSSH: 0, transport: TransportHTTP},
	} {
		overHTTP := &fakeExporter{answer: test.http}
		runner := &fakeSSH{answer: exporting(t, full, only)}
		var clock *manualClock
		snapshotter, clock := newSnapshotter(oneRepoSources("/repos/widgets").collectors(), func(options *Options) {
			options.Remotes = []RemoteTarget{{Machine: vmKey, HTTP: &HTTPRoute{URL: "https://vm.example", TokenFile: "/etc/wb/vm.token"}, SSH: &vmRoute}}
			options.Transports = []RemoteTransport{
				{Name: TransportHTTP, Exporter: overHTTP},
				{Name: TransportSSH, Exporter: NewSSHExporter(foundSSH(t, nil), runner, func() time.Time { return clock.Now() }, nil)},
			}
		})
		overHTTP.clock, runner.clock = clock, clock
		refreshAndSettle(t, snapshotter)
		server := newCockpitServer(t, snapshotter)
		watching(t, snapshotter, clock, test.read(server), 2*time.Hour, true, test.onMachines)
		attempts := append(overHTTP.callsAt(false), overHTTP.callsAt(true)...)
		if got := inTheHourFrom(attempts, 3600); got != test.wantHTTP {
			t.Errorf("%s: %d http reads in the last hour, want %d (at %v)", name, got, test.wantHTTP, attempts)
		}
		if got := inTheHourFrom(runner.seconds(), 3600); got != test.wantSSH {
			t.Errorf("%s: %d ssh logins in the last hour, want %d (at %v)", name, got, test.wantSSH, runner.seconds())
		}
		if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || vm.Route != RouteLiveRemote || vm.Transport != test.transport || vm.RemoteError != test.failure {
			t.Errorf("%s: the machine = %+v, want it live over %s with %q", name, vm, test.transport, test.failure)
		}
	}
}

// fixedReader is a localReader with a fixed answer.
type fixedReader bool

func (f fixedReader) LocalReader(*http.Request) bool { return bool(f) }

// TestOnlyAReaderOnThisMachineIsDemand is the classification itself: the export
// verb's marked read and a request that is not a reader on this machine (the
// hosted page, as cockpit classifies it) are no demand whoever they act as, an
// owner session is demand for both transports and any other reader on this
// machine for HTTP alone. A read that is no demand records nothing and wakes
// nothing.
func TestOnlyAReaderOnThisMachineIsDemand(t *testing.T) {
	t.Parallel()
	owner, anonymous := cockpit.Principal{Name: cockpit.PrincipalOwner}, cockpit.Principal{Name: cockpit.PrincipalAnonymousLocal}
	plain := httptest.NewRequest(http.MethodGet, cockpit.APIPrefix+FleetRoute, nil)
	marked := plain.Clone(t.Context())
	marked.Header.Set(ExportReaderHeader, "1")
	for name, test := range map[string]struct {
		local     fixedReader
		request   *http.Request
		principal cockpit.Principal
		want      demand
	}{
		"an owner on this machine":         {true, plain, owner, demandOwner},
		"an anonymous reader":              {true, plain, anonymous, demandLocal},
		"the export verb, as an owner":     {true, marked, owner, demandNone},
		"the export verb":                  {true, marked, anonymous, demandNone},
		"not a reader on this machine":     {false, plain, anonymous, demandNone},
		"an owner that is not a local one": {false, plain, owner, demandNone},
	} {
		if got := demandOf(test.local, test.request, test.principal); got != test.want {
			t.Errorf("%s: demand = %d, want %d", name, got, test.want)
		}
	}
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	runner := &fakeSSH{answer: exporting(t, full, full)}
	snapshotter, _, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, nil)
	refreshAndSettle(t, snapshotter)
	pollIdle(t, snapshotter)
	vm, _ := machineNamed(snapshotter.Document(), vmKey)
	snapshotter.fleetRead(demandNone)
	snapshotter.metricsRead(vm.ID, demandNone)
	snapshotter.metricsRead("machine-nope", demandOwner)
	if snapshotter.fleetAsked.Load() != 0 || snapshotter.live[vmKey].asked.Load() != 0 || len(snapshotter.kick) != 0 {
		t.Fatal("a read that is no demand was recorded")
	}
	snapshotter.metricsRead(vm.ID, demandLocal)
	if machine := snapshotter.live[vmKey]; machine.asked.Load() == 0 || machine.ownerAsked.Load() != 0 {
		t.Fatal("an anonymous ask for the metrics was recorded as an owner's")
	}
	snapshotter.metricsRead(vm.ID, demandOwner)
	if snapshotter.live[vmKey].ownerAsked.Load() == 0 {
		t.Fatal("an owner's ask for the metrics was not recorded")
	}
}

// TestARefusedLoginBacksOffToAnHourAndOtherFailuresToFiveMinutes is the
// attempts of a failing machine a client is looking at.
func TestARefusedLoginBacksOffToAnHourAndOtherFailuresToFiveMinutes(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		answer func([]string) sshAnswer
		want   []int
	}{
		"a refused login":     {failingSSH(255, "", "Permission denied (publickey)."), []int{0, 120, 360, 840, 1800, 3720, 7320}},
		"an unknown host key": {failingSSH(255, "", "Host key verification failed."), []int{0, 120, 360, 840, 1800, 3720, 7320}},
		"an unreachable host": {failingSSH(255, "", "ssh: connect to host vm.example port 22: Connection refused"), []int{0, 120, 360, 660, 960, 1260, 1560}},
	} {
		runner := &fakeSSH{answer: test.answer}
		snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, nil)
		refreshAndSettle(t, snapshotter)
		watching(t, snapshotter, clock, newCockpitServer(t, snapshotter).asOwner(), 3*time.Hour, true, false)
		if got := runner.seconds(); len(got) < len(test.want) || !slices.Equal(got[:len(test.want)], test.want) {
			t.Errorf("%s: attempts at %v seconds, want %v first", name, got, test.want)
		}
	}
}

// TestTheFirstReaderAfterAQuietTimeGetsAnExportStartedAtOnce: with nobody
// looking the machine is not read at its interval, and its entries stay, with
// their age, until its keepalive. A read of the fleet document is answered from
// what is held (it starts nothing itself), records the demand and wakes the
// loop, whose next look starts one export. A read by the export verb, which is
// another machine's daemon, is not demand.
func TestTheFirstReaderAfterAQuietTimeGetsAnExportStartedAtOnce(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	runner := &fakeSSH{answer: exporting(t, full, full)}
	snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	for range 10 * time.Minute / remoteStep {
		pollIdle(t, snapshotter)
		clock.advance(remoteStep)
	}
	refreshAndSettle(t, snapshotter)
	if runner.count() != 1 {
		t.Fatalf("%d exports in ten minutes with nobody looking, want the first one alone", runner.count())
	}
	if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || vm.Route != RouteLiveRemote || vm.RemoteError != "" || clock.Now().Sub(vm.ObservedAt) != 10*time.Minute {
		t.Fatalf("the idle machine = %+v, want its entries of ten minutes ago", vm)
	}
	// Another machine's daemon reads this one through the export verb.
	if recorder := server.get(cockpit.APIPrefix+FleetRoute, nil, ExportReaderHeader, "1"); recorder.Code != http.StatusOK {
		t.Fatalf("the export verb's read = %d", recorder.Code)
	}
	pollIdle(t, snapshotter)
	if runner.count() != 1 || len(snapshotter.kick) != 0 {
		t.Fatalf("the export verb's read was taken for a person looking: %d exports", runner.count())
	}
	// The hosted page reads the document: it is no demand either.
	server.asHostedPage()(cockpit.APIPrefix + FleetRoute)
	pollIdle(t, snapshotter)
	if runner.count() != 1 || len(snapshotter.kick) != 0 {
		t.Fatalf("the hosted page's read was taken for a reader on this machine: %d exports", runner.count())
	}
	// The owner looks.
	owner := server.owner()
	if recorder := server.get(cockpit.APIPrefix+FleetRoute, owner); recorder.Code != http.StatusOK || runner.count() != 1 {
		t.Fatalf("the read = %d, with %d exports started by it", recorder.Code, runner.count())
	}
	if len(snapshotter.kick) != 1 {
		t.Fatal("the read did not wake the loop")
	}
	// More reads before the loop wakes neither block nor queue more wake-ups.
	clock.advance(remoteStep)
	server.get(cockpit.APIPrefix+FleetRoute, owner)
	server.get(cockpit.APIPrefix+FleetRoute, owner)
	if len(snapshotter.kick) != 1 {
		t.Fatalf("%d wake-ups queued", len(snapshotter.kick))
	}
	pollIdle(t, snapshotter)
	pollIdle(t, snapshotter)
	if runner.count() != 2 {
		t.Fatalf("%d exports after the first reader, want one more", runner.count())
	}
	// With no machine to read, a read wakes nothing.
	alone, _ := newSnapshotter(oneRepoSources("/repos/widgets").collectors(), nil)
	refreshAndSettle(t, alone)
	newCockpitServer(t, alone).get(cockpit.APIPrefix+FleetRoute, nil)
	if len(alone.kick) != 0 {
		t.Fatal("a daemon with no machine to read was woken")
	}
}

// TestAnIdleMachinesEntriesGoWhenItsKeepaliveFails: the longer life of an idle
// machine's entries ends with the first export that brings nothing.
func TestAnIdleMachinesEntriesGoWhenItsKeepaliveFails(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	runner := &fakeSSH{answer: exporting(t, full, full)}
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, cachedVM("alex"))
	snapshotter, clock, _ := newSSHLive(t, sources, runner, sshTotalTimeout, nil)
	refreshAndSettle(t, snapshotter)
	pollIdle(t, snapshotter)
	runner.set(failingSSH(255, "", "ssh: connect to host vm.example port 22: Connection refused"))
	clock.advance(idleKeepalive - remoteStep)
	pollIdle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteLiveRemote || runner.count() != 1 {
		t.Fatalf("just before its keepalive the machine = %+v after %d exports", vm, runner.count())
	}
	clock.advance(remoteStep)
	pollIdle(t, snapshotter)
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteCached || vm.RemoteError != RemoteErrorSSHUnavailable || runner.count() != 2 {
		t.Fatalf("after a failed keepalive the machine = %+v after %d exports", vm, runner.count())
	}
}

// TestAMachineIsNeverReadMoreOftenThanEveryThirtySeconds: every export, of
// either kind, is a login to the machine, and no two start within 30 seconds of
// each other, whatever cockpit.refresh_interval says and whoever asks. A refresh
// interval under 30 seconds is the local snapshot's; the machine's entries are
// held fresh against the 30 seconds.
func TestAMachineIsNeverReadMoreOftenThanEveryThirtySeconds(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	for _, interval := range []time.Duration{10 * time.Second, 45 * time.Second, time.Minute, 70 * time.Second} {
		runner := &fakeSSH{answer: exporting(t, full, only)}
		snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, func(options *Options) { options.Interval = interval })
		refreshAndSettle(t, snapshotter)
		server := newCockpitServer(t, snapshotter)
		// A reader of the fleet document and of the machine's metrics, for an hour.
		for elapsed := time.Duration(0); elapsed < time.Hour; elapsed += remoteStep {
			server.get(cockpit.APIPrefix+FleetRoute, server.owner())
			if vm, found := machineNamed(snapshotter.Document(), vmKey); found {
				server.get(metricsURL+vm.ID, server.owner())
				if vm.Route != RouteLiveRemote {
					t.Fatalf("interval %s: at %s the machine = %+v", interval, elapsed, vm)
				}
			}
			pollIdle(t, snapshotter)
			clock.advance(remoteStep)
		}
		starts := runner.seconds()
		for index := 1; index < len(starts); index++ {
			if starts[index]-starts[index-1] < 30 {
				t.Fatalf("interval %s: logins at %d s and %d s", interval, starts[index-1], starts[index])
			}
		}
		// The floor does not starve the machine: it is read at least once a minute
		// and a half, and with a short interval every 30 seconds.
		if len(starts) < 40 || (interval <= 30*time.Second && len(starts) != 120) {
			t.Errorf("interval %s: %d logins in the hour, at %v", interval, len(starts), starts)
		}
	}
	// The interval itself: with a 10 second refresh interval the fleet is read
	// every 30 seconds.
	runner := &fakeSSH{answer: exporting(t, full, only)}
	snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, func(options *Options) { options.Interval = 10 * time.Second })
	refreshAndSettle(t, snapshotter)
	for range 2 * time.Minute / remoteStep {
		pollAndSettle(t, snapshotter)
		clock.advance(remoteStep)
	}
	if got, want := runner.seconds(), []int{0, 30, 60, 90}; !slices.Equal(got, want) || snapshotter.remoteInterval() != minRemoteInterval {
		t.Fatalf("exports at %v seconds, want %v", got, want)
	}
}

// TestARefusedMetricsLoginHoldsTheFleetLoginBackToo: with a 70 second interval a
// metrics-only export at 30 seconds meets a refused login. The fleet export that
// was due at 70 seconds is not started: SSH is barred for both kinds until the
// refusal's backoff has passed, and the machine shows auth_failed meanwhile.
func TestARefusedMetricsLoginHoldsTheFleetLoginBackToo(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	good := exporting(t, full, full)
	runner := &fakeSSH{answer: func(args []string) sshAnswer {
		if slices.Contains(args, "--metrics-only") {
			return sshAnswer{stderr: []byte("alex@vm.example: Permission denied (publickey)."), err: exitStatus(255)}
		}
		return good(args)
	}}
	snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, func(options *Options) { options.Interval = 70 * time.Second })
	refreshAndSettle(t, snapshotter)
	watching(t, snapshotter, clock, newCockpitServer(t, snapshotter).asOwner(), 4*time.Minute, true, true)
	// The refusal at 30 s bars ssh for 140 s (the interval, doubled).
	if got, want := runner.seconds(), []int{0, 30, 170, 200}; !slices.Equal(got, want) {
		t.Fatalf("logins at %v seconds, want %v", got, want)
	}
	calls := runner.all()
	if slices.Contains(calls[2], "--metrics-only") || !slices.Contains(calls[1], "--metrics-only") {
		t.Fatalf("the login after the bar was not the fleet export: %v", calls[2])
	}
	snapshotter.mu.RLock()
	shown := snapshotter.live[vmKey].remoteError
	snapshotter.mu.RUnlock()
	if shown != RemoteErrorAuthFailed {
		t.Fatalf("the machine shows %q", shown)
	}
}

// TestAFleetReadRightAfterAMetricsLoginWaitsForTheFloor: a read of the fleet
// document that makes the fleet export due one second after a metrics-only
// login wakes the loop, and the export still starts 30 seconds after that login,
// not at once.
func TestAFleetReadRightAfterAMetricsLoginWaitsForTheFloor(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	runner := &fakeSSH{answer: exporting(t, full, only)}
	snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	pollIdle(t, snapshotter)
	vm, _ := machineNamed(snapshotter.Document(), vmKey)
	clock.advance(100 * time.Second)
	server.get(metricsURL+vm.ID, server.owner())
	pollIdle(t, snapshotter)
	clock.advance(time.Second)
	server.get(cockpit.APIPrefix+FleetRoute, server.owner())
	if len(snapshotter.kick) != 1 {
		t.Fatal("the read did not wake the loop")
	}
	pollIdle(t, snapshotter)
	clock.advance(28 * time.Second)
	pollIdle(t, snapshotter)
	if got, want := runner.seconds(), []int{0, 100}; !slices.Equal(got, want) {
		t.Fatalf("logins at %v seconds, want %v: the fleet export did not wait", got, want)
	}
	clock.advance(time.Second)
	pollIdle(t, snapshotter)
	if got, want := runner.seconds(), []int{0, 100, 130}; !slices.Equal(got, want) || slices.Contains(runner.all()[2], "--metrics-only") {
		t.Fatalf("logins at %v seconds, want %v, the last a fleet export", got, want)
	}
}

// TestARefusedSSHLoginDoesNotDelayTheHTTPRetry: the hour is SSH's alone. A
// machine whose HTTP route fails and whose SSH login is refused is still asked
// over HTTP on the five-minute backoff, while SSH is tried again after 2, 4, 8,
// 16, 32 and 60 minutes (at the HTTP attempt that follows each).
func TestARefusedSSHLoginDoesNotDelayTheHTTPRetry(t *testing.T) {
	t.Parallel()
	overHTTP := &fakeExporter{answer: failing(&RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true})}
	runner := &fakeSSH{answer: failingSSH(255, "", "alex@vm.example: Permission denied (publickey).")}
	var clock *manualClock
	snapshotter, clock := newSnapshotter(oneRepoSources("/repos/widgets").collectors(), func(options *Options) {
		options.Remotes = []RemoteTarget{{Machine: vmKey, HTTP: &HTTPRoute{URL: "https://vm.example", TokenFile: "/etc/wb/vm.token"}, SSH: &vmRoute}}
		options.Transports = []RemoteTransport{
			{Name: TransportHTTP, Exporter: overHTTP},
			{Name: TransportSSH, Exporter: NewSSHExporter(foundSSH(t, nil), runner, func() time.Time { return clock.Now() }, nil)},
		}
	})
	overHTTP.clock, runner.clock = clock, clock
	refreshAndSettle(t, snapshotter)
	watching(t, snapshotter, clock, newCockpitServer(t, snapshotter).asOwner(), 3*time.Hour, true, true)
	if got, want := runner.seconds(), []int{0, 120, 360, 960, 2160, 4260, 7860}; !slices.Equal(got, want) {
		t.Errorf("ssh logins at %v seconds, want %v", got, want)
	}
	attempts := overHTTP.callsAt(false)
	if got := inTheHourFrom(attempts, 7200); got != 12 || len(overHTTP.callsAt(true)) != 0 {
		t.Errorf("%d http attempts in the last hour, want one every five minutes (at %v)", got, attempts)
	}
	if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || !slices.Contains(remoteErrorCodes, vm.RemoteError) {
		t.Errorf("the machine = %+v", vm)
	}
}

// TestAFleetReadWakesTheStartedLoop: in the running daemon a read of the fleet
// document that makes an export due starts it without waiting for the loop's
// next step.
func TestAFleetReadWakesTheStartedLoop(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	runner := &fakeSSH{answer: exporting(t, full, full)}
	never := make(chan time.Time)
	snapshotter, clock, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, func(options *Options) {
		options.Collectors.Remote = nil
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return never, func() {} }
		options.RemoteTick = func(time.Duration) (<-chan time.Time, func()) { return never, func() {} }
	})
	server := newCockpitServer(t, snapshotter)
	stop := snapshotter.Start(t.Context())
	t.Cleanup(stop)
	await := func(exports int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for runner.count() < exports {
			if time.Now().After(deadline) {
				t.Fatalf("%d exports, want %d", runner.count(), exports)
			}
			time.Sleep(time.Millisecond)
		}
	}
	await(1)
	// The first export has been recorded once the machine is in the document.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, found := machineNamed(snapshotter.Document(), vmKey); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first export was never recorded")
		}
	}
	clock.advance(10 * time.Minute)
	server.get(cockpit.APIPrefix+FleetRoute, server.owner())
	await(2)
}
