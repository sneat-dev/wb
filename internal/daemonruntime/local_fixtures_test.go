//go:build !windows

package daemonruntime

// TestListenDaemonLocalRefusesNonSocketPath drives listenDaemonLocal's
// `info.Mode()&os.ModeSocket == 0` branch: a plain file already sitting at
// the resolved socket path must be refused rather than clobbered.
//
// The explicit private projects root isolates the socket path; TestMain
// isolates real HOME/configuration, so this case can run in parallel.
