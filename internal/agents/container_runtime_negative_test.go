//go:build linux

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// mustContainerInspectDocument decodes the same one-element array real
// `docker inspect` prints into the struct the controller verifies.
func mustContainerInspectDocument(t *testing.T, raw []byte) containerInspectDocument {
	t.Helper()
	var documents []containerInspectDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatalf("decode container inspect fixture: %v", err)
	}
	if len(documents) != 1 {
		t.Fatalf("container inspect fixture decoded to %d objects", len(documents))
	}
	return documents[0]
}

func validContainerInspectDocument(t *testing.T, policy ExecutionPolicy, reservation ContainerReservation) containerInspectDocument {
	t.Helper()
	return mustContainerInspectDocument(t, testContainerRuntimeDocument(t, reservation.ContainerID, reservation.Name, reservation.RunID, reservation.Nonce, testContainerRuntimeOwner, policy, true))
}

// TestContainerRuntimeRefusesUnusableRuntimeOptions covers the constructor and
// Docker-probe refusals that happen before the durable slot is ever taken.
func TestContainerRuntimeRefusesUnusableRuntimeOptions(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	t.Run("invalid execution policy", func(t *testing.T) {
		t.Parallel()
		options := ContainerRuntimeOptions{RunID: testContainerRunID, Lifetime: time.Hour}
		if _, err := OpenContainerRuntime(context.Background(), options); err == nil {
			t.Fatal("an incomplete execution policy must be refused")
		}
	})

	t.Run("invalid run ID", func(t *testing.T) {
		t.Parallel()
		options := ContainerRuntimeOptions{Policy: policy, RunID: "not-an-agent", Lifetime: time.Hour}
		if _, err := OpenContainerRuntime(context.Background(), options); err == nil {
			t.Fatal("an invalid run ID must be refused")
		} else if !strings.Contains(err.Error(), "run ID") {
			t.Fatalf("error = %v, want the run ID refusal", err)
		}
	})

	t.Run("the default host seam is used when none is injected", func(t *testing.T) {
		t.Parallel()
		options := ContainerRuntimeOptions{
			Policy:   policy,
			RunID:    testContainerRunID,
			Lifetime: time.Hour,
			stateDir: testContainerSlotStateDir(t),
		}
		runtime, err := newContainerRuntime(options)
		if err != nil {
			t.Fatalf("newContainerRuntime with the default host: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		if runtime.host == nil {
			t.Fatal("the default host seam must be installed")
		}
	})

	t.Run("an unusable state directory blocks before Docker", func(t *testing.T) {
		t.Parallel()
		blocked := filepath.Join(t.TempDir(), "state-is-a-file")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatalf("write state path: %v", err)
		}
		fake := newReadyFake(t, policy)
		options := testContainerRuntimeOptions(blocked, fake)
		if _, err := OpenContainerRuntime(context.Background(), options); err == nil {
			t.Fatal("an unusable state directory must refuse the runtime")
		}
		if fake.callCount("context")+fake.callCount("info") != 0 {
			t.Fatalf("an unusable state directory must refuse before any Docker probe: %v", fake.calls)
		}
	})

	t.Run("docker resolver failure", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		options := testContainerRuntimeOptions(testContainerSlotStateDir(t), fake)
		options.host.dockerPath = func() (string, error) { return "", errors.New("docker not on PATH") }
		runtime, err := OpenContainerRuntime(context.Background(), options)
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a Docker resolver failure must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "docker command-line tool") {
			t.Fatalf("error = %v, want the missing docker tool named", err)
		}
	})

	t.Run("empty docker path", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		options := testContainerRuntimeOptions(testContainerSlotStateDir(t), fake)
		options.host.dockerPath = func() (string, error) { return "   ", nil }
		runtime, err := OpenContainerRuntime(context.Background(), options)
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an empty Docker path must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "empty path") {
			t.Fatalf("error = %v, want the empty resolver path named", err)
		}
	})
}

// TestContainerRuntimeOpenRefusesAnUnpinnedDaemon covers the open-time refusal
// of a context that cannot be pinned and of a daemon identity that cannot be
// read. The command seam is injected; no real Docker is touched.
func TestContainerRuntimeOpenRefusesAnUnpinnedDaemon(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	t.Run("a nil context still opens normally", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := OpenContainerRuntime(nil, testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime with a nil context: %v", err)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("an unpinnable context blocks before any container", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		options := testContainerRuntimeOptions(testContainerSlotStateDir(t), fake)
		// No injected command seam: the default Docker command wrapper is built,
		// then the resolver is pointed at a harmless failsafe binary so no real
		// Docker daemon is contacted.
		options.commands = nil
		options.host.dockerPath = func() (string, error) { return "/bin/false", nil }
		runtime, err := OpenContainerRuntime(context.Background(), options)
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an unpinnable Docker context must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "Docker context") {
			t.Fatalf("error = %v, want the unpinned context refusal", err)
		}
	})

	t.Run("unreadable daemon identity", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		fake.daemonErr = errors.New("docker info transport failed")
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an unreadable daemon identity must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "Docker Engine") {
			t.Fatalf("error = %v, want the failed daemon identity named", err)
		}
		if fake.createCount() != 0 {
			t.Fatal("an unreadable daemon must block before create")
		}
	})

	t.Run("image inspection failure", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		fake.imageErr = errors.New("image inspect transport failed")
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an unreadable image must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "image") {
			t.Fatalf("error = %v, want the image inspection failure named", err)
		}
		if fake.createCount() != 0 {
			t.Fatal("a refused image must block before create")
		}
	})
}

// erroredAttachProcess models an attach client that fails immediately.
type erroredAttachProcess struct{ err error }

func (process *erroredAttachProcess) Wait() error { return process.err }
func (process *erroredAttachProcess) Kill() error { return nil }

// TestContainerRuntimeWaitsForARunningVerifiedContainer covers the exited,
// timed-out and polled inspect paths while waiting for a fresh container.
func TestContainerRuntimeWaitsForARunningVerifiedContainer(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	t.Run("an exited container is refused before ready", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		fake.onCreate = func(args []string, containerID string) {
			name, runID, nonce, owner := testContainerCreateIdentity(t, args)
			fake.setDocument(containerID, testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, false))
		}
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a container that exited before ready must be refused")
		}
		if !strings.Contains(err.Error(), "exited before it became ready") {
			t.Fatalf("error = %v, want the early-exit refusal", err)
		}
	})

	t.Run("an already-canceled wait reports the bounded timeout", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		fake.onCreate = func(args []string, containerID string) {
			name, runID, nonce, owner := testContainerCreateIdentity(t, args)
			document := testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, false)
			parsed := mustContainerInspectDocument(t, document)
			parsed.State.Status = "created"
			raw, err := json.Marshal([]any{parsed})
			if err != nil {
				t.Fatalf("marshal created-state document: %v", err)
			}
			fake.setDocument(containerID, raw)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runtime, err := OpenContainerRuntime(ctx, testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a canceled wait must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "did not become running") {
			t.Fatalf("error = %v, want the bounded start timeout", err)
		}
	})

	t.Run("the poll loop accepts a container that starts running", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		var (
			name, runID, nonce, owner string
			inspections               int
		)
		fake.onCreate = func(args []string, containerID string) {
			name, runID, nonce, owner = testContainerCreateIdentity(t, args)
			document := testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, false)
			parsed := mustContainerInspectDocument(t, document)
			parsed.State.Status = "created"
			raw, err := json.Marshal([]any{parsed})
			if err != nil {
				t.Fatalf("marshal created-state document: %v", err)
			}
			fake.setDocument(containerID, raw)
		}
		fake.onInspect = func(identity string) {
			inspections++
			if inspections == 2 {
				fake.setDocument(identity, testContainerRuntimeDocument(t, identity, name, runID, nonce, owner, policy, true))
			}
		}
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime after the container started: %v", err)
		}
		if inspections < 2 {
			t.Fatalf("inspect count = %d, want the poll loop to reach running", inspections)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("an attach client that exits before ready is refused", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		fake.attachErr = nil
		fake.attachFactory = func(context.Context, *os.File) (containerAttachProcess, error) {
			return &erroredAttachProcess{err: errors.New("attach exited early")}, nil
		}
		var (
			name, runID, nonce, owner string
			inspections               int
		)
		fake.onCreate = func(args []string, containerID string) {
			name, runID, nonce, owner = testContainerCreateIdentity(t, args)
			document := testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, false)
			parsed := mustContainerInspectDocument(t, document)
			parsed.State.Status = "created"
			raw, err := json.Marshal([]any{parsed})
			if err != nil {
				t.Fatalf("marshal created-state document: %v", err)
			}
			fake.setDocument(containerID, raw)
		}
		fake.onInspect = func(identity string) {
			inspections++
			if inspections == 2 {
				fake.setDocument(identity, testContainerRuntimeDocument(t, identity, name, runID, nonce, owner, policy, true))
			}
		}
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an attach client that exited before ready must be refused")
		}
		if !strings.Contains(err.Error(), "exited before the container became ready") {
			t.Fatalf("error = %v, want the early attach exit named", err)
		}
	})
}

// TestContainerRuntimeRefusesMalformedCreateOutcomes drives every bad `docker
// create` / inspect answer through the real Open path.
func TestContainerRuntimeRefusesMalformedCreateOutcomes(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	setRawDocument := func(fake *fakeContainerDocker, raw []byte) {
		fake.onCreate = func(_ []string, containerID string) {
			fake.setDocument(containerID, raw)
		}
	}

	cases := map[string]struct {
		configure func(*fakeContainerDocker, *testing.T)
		want      string
	}{
		"create returns a non-hex identity": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) { fake.createID = "not-a-container-id" },
			want:      "not a full 64-character lowercase hex container ID",
		},
		"inspect answer above the byte bound": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) {
				setRawDocument(fake, make([]byte, maxContainerInspectBytes+1))
			},
			want: "above the",
		},
		"inspect answer is not JSON": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) { setRawDocument(fake, []byte("{")) },
			want:      "not valid JSON",
		},
		"inspect answer has no objects": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) { setRawDocument(fake, []byte("[]")) },
			want:      "exactly one container was expected",
		},
		"container ID does not match the reservation": {
			configure: func(fake *fakeContainerDocker, t *testing.T) {
				fake.onCreate = func(args []string, _ string) {
					name, runID, nonce, owner := testContainerCreateIdentity(t, args)
					fake.setDocument(testContainerID, testContainerRuntimeDocument(t, testContainerID2, name, runID, nonce, owner, policy, true))
				}
			},
			want: "does not match the reserved container ID",
		},
		"PID1 runs as the image user": {
			configure: func(fake *fakeContainerDocker, t *testing.T) {
				fake.onCreate = func(args []string, containerID string) {
					name, runID, nonce, owner := testContainerCreateIdentity(t, args)
					document := mustContainerInspectDocument(t, testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, true))
					document.Config.User = "1000:1000"
					raw, err := json.Marshal([]any{document})
					if err != nil {
						t.Fatalf("marshal PID1 document: %v", err)
					}
					fake.setDocument(containerID, raw)
				}
			},
			want: "watchdog must run as",
		},
		"cgroup exec fails": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) {
				fake.execErr = errors.New("cgroup exec transport failed")
			},
			want: "read the container cgroup limits",
		},
		"cgroup values are missing": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) {
				fake.execOutput = []byte("\n\n")
			},
			want: "no cgroup values",
		},
		"cgroup lines are incomplete": {
			configure: func(fake *fakeContainerDocker, _ *testing.T) {
				fake.execOutput = []byte("1\n2\n")
			},
			want: "expected the four policy files",
		},
	}
	for name, current := range cases {
		name, current := name, current
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newReadyFake(t, policy)
			current.configure(fake, t)
			runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
			if err == nil {
				_ = runtime.Close()
				t.Fatalf("%s must refuse the runtime", name)
			}
			if !strings.Contains(err.Error(), current.want) {
				t.Fatalf("error = %v, want %q", err, current.want)
			}
		})
	}
}

// TestContainerRuntimeRecordsTheContainerIDFailure keeps the create identity
// path honest: the durable reservation cannot record the ID, so the open must
// fail rather than continue with an unrecorded container.
func TestContainerRuntimeRecordsTheContainerIDFailure(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	fake.onCreate = func(args []string, containerID string) {
		name, runID, nonce, owner := testContainerCreateIdentity(t, args)
		fake.setDocument(containerID, testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, true))
		// The reservation is durable before create, so replace its record with a
		// directory: the follow-up record then fails with EISDIR regardless of the
		// test user's privileges.
		path := filepath.Join(stateDir, containerSlotReservationFileName)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove the written reservation after create: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("block the reservation record after create: %v", err)
		}
	}
	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err == nil {
		_ = runtime.Close()
		t.Fatal("a reservation that cannot record the container ID must refuse the runtime")
	}
	if !strings.Contains(err.Error(), "record the returned container ID") {
		t.Fatalf("error = %v, want the record failure named", err)
	}
}

// TestContainerRuntimeCleanupRefusalsRetainTheDurableBlock covers the teardown
// error alternatives: an inspect that cannot verify the container, an absence
// proof that cannot complete, and a reservation clear that fails.
func TestContainerRuntimeCleanupRefusalsRetainTheDurableBlock(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	t.Run("inspect failure during cleanup", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		fake.attachErr = errors.New("attach failed")
		fake.inspectErrors[testContainerID] = errors.New("docker transport still uncertain")
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an unverifiable cleanup inspect must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "verify the owned container") {
			t.Fatalf("error = %v, want the cleanup verification failure named", err)
		}
		assertReservationRetained(t, stateDir, testContainerID)
	})

	t.Run("absence proof failure during cleanup", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		fake.execOutput = []byte("1\n0\n200000 100000\n128\n")
		inspections := 0
		fake.onInspect = func(identity string) {
			if identity != testContainerID {
				return
			}
			inspections++
			if inspections >= 3 {
				fake.mutex.Lock()
				fake.inspectErrors[identity] = errors.New("absence proof transport failed")
				fake.mutex.Unlock()
			}
		}
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a failed cleanup absence proof must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "could not be proven absent") {
			t.Fatalf("error = %v, want the failed absence proof named", err)
		}
		assertReservationRetained(t, stateDir, testContainerID)
	})

	t.Run("reservation clear failure during cleanup", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		fake.execOutput = []byte("1\n0\n200000 100000\n128\n")
		inspections := 0
		fake.onInspect = func(identity string) {
			if identity != testContainerID {
				return
			}
			inspections++
			if inspections >= 4 {
				fake.mutex.Lock()
				fake.inspectErrors[identity] = errors.New("clear proof transport failed")
				fake.mutex.Unlock()
			}
		}
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a failed reservation clear must refuse the runtime")
		}
		if !strings.Contains(err.Error(), "clear the container reservation after cleanup") {
			t.Fatalf("error = %v, want the failed clear named", err)
		}
		assertReservationRetained(t, stateDir, testContainerID)
	})

	t.Run("an already absent container is proven absent without removal", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		fake.createErr = errors.New("docker create failed after the container was created")
		fake.onCreate = nil
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("the rejected create must refuse the runtime")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("an already absent container must not be removed again: %v", removed)
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot after cleanup: %v", openErr)
		}
		t.Cleanup(func() { _ = slot.Close() })
		if reservation, ok := slot.Reservation(); ok {
			t.Fatalf("cleanup must clear the reservation after proving absence, still holds %#v", reservation)
		}
	})
}

func assertReservationRetained(t *testing.T, stateDir, containerID string) {
	t.Helper()
	slot, err := OpenContainerSlot(stateDir)
	if err != nil {
		t.Fatalf("OpenContainerSlot after failed cleanup: %v", err)
	}
	defer func() { _ = slot.Close() }()
	reservation, ok := slot.Reservation()
	if !ok {
		t.Fatal("a failed cleanup must retain the durable reservation")
	}
	if reservation.ContainerID != containerID {
		t.Fatalf("retained reservation container ID = %q, want %q", reservation.ContainerID, containerID)
	}
}

// TestContainerRuntimeCleanupUsesTheRuntimeReservation covers the cleanup
// fallback to the in-memory reservation, the missing-identity refusal, and the
// clear-path daemon mismatch.
func TestContainerRuntimeCleanupUsesTheRuntimeReservation(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	ctx := context.Background()

	t.Run("falls back to the in-memory reservation", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		reservation := testContainerReservation()
		reservation.ContainerID = testContainerID
		runtime.reservation = reservation
		fake.setDocument(reservation.ContainerID, testContainerRuntimeDocument(t, reservation.ContainerID, reservation.Name, reservation.RunID, reservation.Nonce, testContainerRuntimeOwner, policy, false))

		proven, err := runtime.removeOwnedContainerLocked(ctx)
		if err != nil {
			t.Fatalf("removeOwnedContainerLocked: %v", err)
		}
		if proven != reservation.ContainerID {
			t.Fatalf("proven identity = %q, want %q", proven, reservation.ContainerID)
		}
		if got := fake.removedIDs(); !reflect.DeepEqual(got, []string{reservation.ContainerID}) {
			t.Fatalf("removed = %v, want the in-memory reservation identity", got)
		}
	})

	t.Run("refuses cleanup with no identity at all", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		if _, err := runtime.removeOwnedContainerLocked(ctx); err == nil {
			t.Fatal("cleanup with no reserved identity must refuse")
		} else if !strings.Contains(err.Error(), "no reserved identity") {
			t.Fatalf("error = %v, want the missing identity refusal", err)
		}
		if len(fake.removedIDs()) != 0 {
			t.Fatalf("cleanup with no identity must not remove anything: %v", fake.removedIDs())
		}
	})

	t.Run("clear refuses a daemon mismatch before proving absence", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		reservation := testContainerReservation()
		if err := runtime.slot.Reserve(reservation); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := runtime.slot.RecordContainerID(testContainerID); err != nil {
			t.Fatalf("RecordContainerID: %v", err)
		}
		fake.daemonErr = errors.New("docker info dropped")
		if err := runtime.clearProvenReservationLocked(ctx, testContainerID); err == nil {
			t.Fatal("a daemon mismatch must refuse the reservation clear")
		} else if !strings.Contains(err.Error(), "Docker Engine") {
			t.Fatalf("error = %v, want the failed daemon identity named", err)
		}
		if _, ok := runtime.slot.Reservation(); !ok {
			t.Fatal("a refused clear must retain the reservation")
		}
	})
}

// TestContainerRuntimeCloseAndAccessorGuards covers the nil-receiver and
// already-closed refusals plus the failed writer close that keeps the block.
func TestContainerRuntimeCloseAndAccessorGuards(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	if err := (*ContainerRuntime)(nil).Close(); err != nil {
		t.Fatalf("Close on a nil runtime = %v, want nil", err)
	}
	var nilRuntime *ContainerRuntime
	if got := nilRuntime.ContainerID(); got != "" {
		t.Fatalf("ContainerID on a nil runtime = %q, want empty", got)
	}
	if got := nilRuntime.ImageID(); got != "" {
		t.Fatalf("ImageID on a nil runtime = %q, want empty", got)
	}
	if got := nilRuntime.VerifiedLimits(); got != (ContainerVerifiedLimits{}) {
		t.Fatalf("VerifiedLimits on a nil runtime = %#v, want zero", got)
	}
	if _, err := nilRuntime.ExecArgv([]string{"dsh"}); err == nil {
		t.Fatal("ExecArgv on a nil runtime must refuse")
	} else if !strings.Contains(err.Error(), "runtime is nil") {
		t.Fatalf("error = %v, want the nil runtime refusal", err)
	}

	t.Run("second Close returns the first result", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime: %v", err)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("second Close = %v, want the first result", err)
		}
	})

	t.Run("ExecArgv refuses before a verified container exists", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		if _, err := runtime.ExecArgv([]string{"dsh"}); err == nil {
			t.Fatal("ExecArgv without a verified container must refuse")
		} else if !strings.Contains(err.Error(), "no verified container") {
			t.Fatalf("error = %v, want the missing container refusal", err)
		}
	})

	t.Run("ExecArgv refuses a NUL argument", func(t *testing.T) {
		t.Parallel()
		runtime := &ContainerRuntime{containerID: testContainerID, ownerUser: testContainerRuntimeOwner}
		if _, err := runtime.ExecArgv([]string{"dsh", "bad\x00arg"}); err == nil {
			t.Fatal("ExecArgv must refuse a NUL byte in an argument")
		} else if !strings.Contains(err.Error(), "NUL byte") {
			t.Fatalf("error = %v, want the NUL byte refusal", err)
		}
	})

	t.Run("a failed writer close keeps the durable block", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime: %v", err)
		}
		if err := runtime.writer.Close(); err != nil {
			t.Fatalf("close the writer before Close: %v", err)
		}
		if err := runtime.Close(); err == nil {
			t.Fatal("Close must report the failed writer close")
		} else if !strings.Contains(err.Error(), "close the container attach stdin") {
			t.Fatalf("Close error = %v, want the writer close failure named", err)
		}
		assertReservationRetained(t, stateDir, testContainerID)
	})
}

// TestReapAttachReleasesAnOrphanedCancel covers the cancel-only reap path.
func TestReapAttachReleasesAnOrphanedCancel(t *testing.T) {
	t.Parallel()
	canceled := false
	runtime := &ContainerRuntime{attachCancel: func() { canceled = true }}
	if err := runtime.reapAttachLocked(); err != nil {
		t.Fatalf("reapAttachLocked: %v", err)
	}
	if !canceled {
		t.Fatal("an attach cancel without a client must be released")
	}
	if runtime.attachCancel != nil {
		t.Fatal("the released cancel must be cleared")
	}
}

// TestAttachExitedEarlyReportsTheClientOutcome covers the early-exit reporter
// directly so both the error and the bare exit are observable.
func TestAttachExitedEarlyReportsTheClientOutcome(t *testing.T) {
	t.Parallel()
	if err := (&ContainerRuntime{}).attachExitedEarly(); err != nil {
		t.Fatalf("attachExitedEarly with no client = %v, want nil", err)
	}
	withError := &ContainerRuntime{containerID: testContainerID, attachDone: make(chan error, 1)}
	withError.attachDone <- errors.New("attach transport failed")
	if err := withError.attachExitedEarly(); err == nil {
		t.Fatal("an attach client error must be reported")
	} else if !strings.Contains(err.Error(), "attach transport failed") {
		t.Fatalf("error = %v, want the attach client error wrapped", err)
	}
	withoutError := &ContainerRuntime{containerID: testContainerID, attachDone: make(chan error, 1)}
	withoutError.attachDone <- nil
	if err := withoutError.attachExitedEarly(); err == nil {
		t.Fatal("an attach client that exited must be reported")
	} else if !strings.Contains(err.Error(), "exited before the container became ready") {
		t.Fatalf("error = %v, want the bare early exit named", err)
	}
}

// TestVerifyContainerOwnershipRefusesEachDurableMismatch pins the identity
// fields that must never be trusted from a lookalike container.
func TestVerifyContainerOwnershipRefusesEachDurableMismatch(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	reservation := testContainerReservation()
	reservation.ContainerID = testContainerID
	base := validContainerInspectDocument(t, policy, reservation)

	cases := map[string]struct {
		mutate func(*containerInspectDocument)
		want   string
	}{
		"non-hex container ID": {mutate: func(document *containerInspectDocument) { document.ID = "short" }, want: "full 64-character lowercase hex ID"},
		"wrong name":           {mutate: func(document *containerInspectDocument) { document.Name = "/other" }, want: "not the reserved name"},
		"wrong run label":      {mutate: func(document *containerInspectDocument) { document.Config.Labels[containerLabelRunID] = "other" }, want: "run label"},
		"wrong nonce label":    {mutate: func(document *containerInspectDocument) { document.Config.Labels[containerLabelNonce] = "other" }, want: "nonce label"},
		"wrong user label":     {mutate: func(document *containerInspectDocument) { document.Config.Labels[containerLabelUser] = "1:1" }, want: "user label"},
	}
	for name, current := range cases {
		name, current := name, current
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := base
			document.Config.Labels = map[string]string{
				containerLabelRunID: reservation.RunID,
				containerLabelNonce: reservation.Nonce,
				containerLabelUser:  testContainerRuntimeOwner,
			}
			current.mutate(&document)
			if err := verifyContainerOwnership(document, reservation, testContainerRuntimeOwner); err == nil {
				t.Fatalf("%s must be refused", name)
			} else if !strings.Contains(err.Error(), current.want) {
				t.Fatalf("error = %v, want %q", err, current.want)
			}
		})
	}
	if err := verifyContainerOwnership(base, reservation, testContainerRuntimeOwner); err != nil {
		t.Fatalf("the matching document must verify: %v", err)
	}
}

// TestVerifyContainerHostConfigRefusesEachUnsafeField pins the applied-bounds
// verifier to the mandatory policy fields it models.
func TestVerifyContainerHostConfigRefusesEachUnsafeField(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	reservation := testContainerReservation()
	reservation.ContainerID = testContainerID
	base := validContainerInspectDocument(t, policy, reservation)

	cases := map[string]struct {
		mutate func(*containerInspectDocument)
		want   string
	}{
		"memory":             {mutate: func(document *containerInspectDocument) { document.HostConfig.Memory++ }, want: "memory_bytes"},
		"memory swap":        {mutate: func(document *containerInspectDocument) { document.HostConfig.MemorySwap++ }, want: "memory_swap_bytes"},
		"missing pids limit": {mutate: func(document *containerInspectDocument) { document.HostConfig.PidsLimit = nil }, want: "no pids limit"},
		"pids limit": {mutate: func(document *containerInspectDocument) {
			wrong := int64(policy.PIDsLimit + 1)
			document.HostConfig.PidsLimit = &wrong
		}, want: "pids limit"},
		"privileged":          {mutate: func(document *containerInspectDocument) { document.HostConfig.Privileged = true }, want: "privileged"},
		"auto-remove":         {mutate: func(document *containerInspectDocument) { document.HostConfig.AutoRemove = true }, want: "auto-removed"},
		"restart policy":      {mutate: func(document *containerInspectDocument) { document.HostConfig.Restart.Name = "always" }, want: "restart policy"},
		"user namespace mode": {mutate: func(document *containerInspectDocument) { document.HostConfig.UsernsMode = "host" }, want: "user namespace mode"},
		"published ports": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.PortBindings = map[string]json.RawMessage{"80/tcp": json.RawMessage("[]")}
		}, want: "publishes ports"},
		"capabilities kept": {mutate: func(document *containerInspectDocument) { document.HostConfig.CapDrop = []string{"CHOWN"} }, want: "drop all capabilities"},
		"no-new-privileges": {mutate: func(document *containerInspectDocument) { document.HostConfig.SecurityOpt = []string{} }, want: "no-new-privileges"},
	}
	for name, current := range cases {
		name, current := name, current
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := base
			document.HostConfig.CapDrop = append([]string(nil), base.HostConfig.CapDrop...)
			document.HostConfig.SecurityOpt = append([]string(nil), base.HostConfig.SecurityOpt...)
			current.mutate(&document)
			if err := verifyContainerHostConfig(policy, document, nil); err == nil {
				t.Fatalf("%s must be refused", name)
			} else if !strings.Contains(err.Error(), current.want) {
				t.Fatalf("error = %v, want %q", err, current.want)
			}
		})
	}
	if err := verifyContainerHostConfig(policy, base, nil); err != nil {
		t.Fatalf("the matching host config must verify: %v", err)
	}
}

// TestContainerRuntimeBoundedOutputExactErrorsAndCgroupParsing covers the
// bounded-output limit, Docker error unwrapping, the exact no-such-object
// match, and the cgroup parser's refusals directly.
func TestContainerRuntimeBoundedOutputExactErrorsAndCgroupParsing(t *testing.T) {
	t.Parallel()

	t.Run("bounded output refuses data above the limit", func(t *testing.T) {
		t.Parallel()
		output := &boundedContainerOutput{limit: 3}
		if _, err := output.Write([]byte("abc")); err != nil {
			t.Fatalf("Write within the bound: %v", err)
		}
		if _, err := output.Write([]byte("de")); !errors.Is(err, errContainerOutputExceeded) {
			t.Fatalf("Write above the bound = %v, want errContainerOutputExceeded", err)
		}
		if !output.exceeded {
			t.Fatal("an over-bound write must mark the output exceeded")
		}
	})

	t.Run("docker error unwraps its cause", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("exit status 1")
		err := &containerDockerError{Args: []string{"rm", "cid"}, Stderr: "boom", Err: sentinel}
		if !errors.Is(err, sentinel) {
			t.Fatal("containerDockerError must unwrap its cause")
		}
	})

	t.Run("no-such-object accepts only the exact answer", func(t *testing.T) {
		t.Parallel()
		cases := map[string]struct {
			err      error
			identity string
			want     bool
		}{
			"exact":          {err: testContainerNoSuchObject(testContainerID), identity: testContainerID, want: true},
			"wrong identity": {err: testContainerNoSuchObject(testContainerID), identity: testContainerID2, want: false},
			"empty identity": {err: testContainerNoSuchObject(testContainerID), identity: "", want: false},
			"generic error":  {err: errors.New("docker inspect failed"), identity: testContainerID, want: false},
			"nil error":      {err: nil, identity: testContainerID, want: false},
		}
		for name, current := range cases {
			name, current := name, current
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				if got := containerNoSuchObject(current.err, current.identity); got != current.want {
					t.Fatalf("containerNoSuchObject = %t, want %t", got, current.want)
				}
			})
		}
	})

	t.Run("cgroup parser refuses empty and incomplete answers", func(t *testing.T) {
		t.Parallel()
		if _, err := parseContainerCgroupValues([]byte("   \n")); err == nil {
			t.Fatal("an empty cgroup answer must be refused")
		} else if !strings.Contains(err.Error(), "no cgroup values") {
			t.Fatalf("error = %v, want the empty answer named", err)
		}
		if _, err := parseContainerCgroupValues([]byte("1\n2\n")); err == nil {
			t.Fatal("an incomplete cgroup answer must be refused")
		} else if !strings.Contains(err.Error(), "expected the four policy files") {
			t.Fatalf("error = %v, want the incomplete answer named", err)
		}
	})

	t.Run("daemon confirmation refuses a reservation with no engine identity", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, executionTestPolicy())
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		reservation := testContainerReservation()
		reservation.DaemonID = ""
		if err := runtime.confirmDaemonIdentity(context.Background(), reservation); err == nil {
			t.Fatal("a reservation without a daemon identity must be refused")
		}
	})

	t.Run("image inspection failure", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, executionTestPolicy())
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		fake.imageErr = errors.New("image inspect dropped")
		if _, err := runtime.inspectImage(context.Background()); err == nil {
			t.Fatal("a failed image inspect must be reported")
		}
	})
}

// TestContainerRuntimeBoundedDockerCommand bounds the process output it keeps
// from a real child. The child is a harmless shell, never Docker.
func TestContainerRuntimeBoundedDockerCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	if _, err := runBoundedDockerCommand(ctx, "/bin/sh", nil, nil); err == nil {
		t.Fatal("a command with no arguments must be refused")
	}

	output, err := runBoundedDockerCommand(ctx, "/bin/sh", nil, []string{"-c", "printf hello"})
	if err != nil {
		t.Fatalf("runBoundedDockerCommand success: %v", err)
	}
	if string(output) != "hello" {
		t.Fatalf("output = %q, want hello", output)
	}

	_, err = runBoundedDockerCommand(ctx, "/bin/sh", nil, []string{"-c", "printf boom >&2; exit 3"})
	var dockerErr *containerDockerError
	if !errors.As(err, &dockerErr) {
		t.Fatalf("failed command error = %v, want *containerDockerError", err)
	}
	if !strings.Contains(dockerErr.Stderr, "boom") {
		t.Fatalf("stderr = %q, want the child diagnostics retained", dockerErr.Stderr)
	}

	_, err = runBoundedDockerCommand(ctx, "/bin/sh", nil, []string{"-c", "head -c 9000 /dev/zero >&2; exit 1"})
	if !errors.As(err, &dockerErr) {
		t.Fatalf("truncated stderr error = %v, want *containerDockerError", err)
	}
	if !strings.Contains(dockerErr.Stderr, "diagnostics truncated") {
		t.Fatalf("stderr = %q, want the truncation marker", dockerErr.Stderr)
	}

	_, err = runBoundedDockerCommand(ctx, "/bin/sh", nil, []string{"-c", fmt.Sprintf("head -c %d /dev/zero", maxContainerCommandBytes+1)})
	if err == nil {
		t.Fatal("output above the bound must be refused")
	} else if !strings.Contains(err.Error(), "above the") {
		t.Fatalf("error = %v, want the output bound named", err)
	}
}

// TestContainerRuntimeDefaultHostAndCommandsWiring covers the production seam
// builders without contacting a Docker daemon: the default host resolves the
// real host facts, and the default command closures run an injected harmless
// path.
func TestContainerRuntimeDefaultHostAndCommandsWiring(t *testing.T) {
	t.Parallel()

	host := defaultContainerHost()
	if host.dockerPath == nil || host.hostOS == nil || host.currentUser == nil || host.userHome == nil || host.lookupEnv == nil || host.evalSymlinks == nil {
		t.Fatal("the default host must install every seam")
	}
	home, err := host.homeDirectory()
	if err != nil {
		t.Fatalf("default host home directory: %v", err)
	}
	if home == "" {
		t.Fatal("the default host must resolve a home directory")
	}

	commands := defaultContainerCommands("/bin/sh", []string{"PATH=/usr/bin:/bin"})
	if commands.command == nil || commands.attach == nil {
		t.Fatal("the default command seam must install both command and attach")
	}
	output, err := commands.command(context.Background(), []string{"-c", "printf wired"})
	if err != nil {
		t.Fatalf("default command closure: %v", err)
	}
	if string(output) != "wired" {
		t.Fatalf("default command output = %q, want wired", output)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create attach pipe: %v", err)
	}
	process, err := commands.attach(context.Background(), "unused-container", readEnd)
	_ = readEnd.Close()
	_ = writeEnd.Close()
	if err != nil {
		t.Fatalf("default attach closure must start the child: %v", err)
	}
	_ = process.Kill()
	_ = process.Wait()

	missing := defaultContainerCommands("/definitely/not/a/docker/binary", nil)
	readEnd, writeEnd, err = os.Pipe()
	if err != nil {
		t.Fatalf("create attach pipe: %v", err)
	}
	if _, err := missing.attach(context.Background(), "unused-container", readEnd); err == nil {
		t.Fatal("the attach closure must report a child that cannot start")
	}
	_ = readEnd.Close()
	_ = writeEnd.Close()
}

// killThenExitProcess models an attach client that ignores the closed pipe but
// exits once it is killed.
type killThenExitProcess struct {
	killed chan struct{}
	once   sync.Once
}

func (process *killThenExitProcess) Wait() error {
	<-process.killed
	return nil
}

func (process *killThenExitProcess) Kill() error {
	process.once.Do(func() { close(process.killed) })
	return nil
}

// TestContainerRuntimeRecoveryReservationAndAttachBounds covers the recovery
// alternatives, the reservation-record refusal, the bounded attach reap and the
// default host wiring that the fixture command seam can still reach. A scripted
// harmless child stands in for Docker where the default command wrapper must be
// built.
func TestContainerRuntimeRecoveryReservationAndAttachBounds(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	ctx := context.Background()

	t.Run("an invalid workspace refuses the constructor", func(t *testing.T) {
		t.Parallel()
		options := testContainerRuntimeOptions(testContainerSlotStateDir(t), newReadyFake(t, policy))
		options.Workspace = &ContainerWorkspace{WorktreeDir: "/", GitCommonDir: "/tmp"}
		if _, err := OpenContainerRuntime(ctx, options); err == nil {
			t.Fatal("an invalid workspace must refuse the constructor")
		}
	})

	t.Run("a reservation that cannot be written blocks before create", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		// A directory at the reservation path makes every write fail even for a
		// privileged test runner.
		reservationPath := filepath.Join(stateDir, containerSlotReservationFileName)
		if err := os.Mkdir(reservationPath, 0o700); err != nil {
			t.Fatalf("block the reservation path: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(reservationPath) })
		if err := runtime.open(ctx); err == nil {
			t.Fatal("an unwritable durable reservation must block before create")
		}
		if fake.createCount() != 0 {
			t.Fatal("an unwritable reservation must block before create")
		}
	})

	t.Run("a sub-second lifetime is passed as one second", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		options := testContainerRuntimeOptions(testContainerSlotStateDir(t), fake)
		options.Lifetime = 500 * time.Millisecond
		runtime, err := OpenContainerRuntime(ctx, options)
		if err != nil {
			t.Fatalf("OpenContainerRuntime: %v", err)
		}
		defer func() { _ = runtime.Close() }()
		args := fake.createCall(t)
		if args[len(args)-1] != "1s" {
			t.Fatalf("lifetime argument = %q, want the one-second floor", args[len(args)-1])
		}
	})

	t.Run("a prior reservation that names only a container name proves it absent", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		runtime.daemonID = testContainerDaemonID
		prior := testContainerReservation()
		if err := runtime.slot.Reserve(prior); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := runtime.recoverPriorReservation(ctx); err != nil {
			t.Fatalf("recoverPriorReservation by name: %v", err)
		}
		if _, ok := runtime.slot.Reservation(); ok {
			t.Fatal("the absent prior reservation must be cleared")
		}
	})

	t.Run("a stale clear refuses a daemon mismatch", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		runtime.daemonID = testContainerDaemonID
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		if err := runtime.slot.Reserve(testContainerReservation()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := runtime.slot.RecordContainerID(prior.ContainerID); err != nil {
			t.Fatalf("RecordContainerID: %v", err)
		}
		fake.daemonID = testContainerDaemonID2
		if err := runtime.recoverPriorReservation(ctx); err == nil {
			t.Fatal("a stale clear on a switched daemon must refuse")
		} else if !strings.Contains(err.Error(), "clear the stale container reservation") {
			t.Fatalf("error = %v, want the stale clear refusal", err)
		}
	})

	t.Run("a stopped prior container refuses a daemon mismatch before removal", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		runtime.daemonID = testContainerDaemonID
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		if err := runtime.slot.Reserve(testContainerReservation()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := runtime.slot.RecordContainerID(prior.ContainerID); err != nil {
			t.Fatalf("RecordContainerID: %v", err)
		}
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))
		fake.daemonID = testContainerDaemonID2
		if err := runtime.recoverPriorReservation(ctx); err == nil {
			t.Fatal("a daemon mismatch before removal must refuse")
		} else if !strings.Contains(err.Error(), "confirm the daemon") {
			t.Fatalf("error = %v, want the daemon confirmation failure", err)
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("a daemon mismatch must not remove the prior container: %v", removed)
		}
	})

	t.Run("a recovered clear refuses a daemon switch after removal", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		runtime.daemonID = testContainerDaemonID
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		if err := runtime.slot.Reserve(testContainerReservation()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := runtime.slot.RecordContainerID(prior.ContainerID); err != nil {
			t.Fatalf("RecordContainerID: %v", err)
		}
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))
		infos := 0
		fake.onInfo = func() {
			infos++
			if infos >= 2 {
				fake.mutex.Lock()
				fake.daemonID = testContainerDaemonID2
				fake.mutex.Unlock()
			}
		}
		if err := runtime.recoverPriorReservation(ctx); err == nil {
			t.Fatal("a daemon switch before the clear must refuse")
		} else if !strings.Contains(err.Error(), "clear the recovered container reservation") {
			t.Fatalf("error = %v, want the recovered clear refusal", err)
		}
		if _, ok := runtime.slot.Reservation(); !ok {
			t.Fatal("a refused recovered clear must retain the reservation")
		}
	})

	t.Run("an empty identity is refused by the absence proof", func(t *testing.T) {
		t.Parallel()
		runtime := &ContainerRuntime{}
		if err := runtime.proveAbsent(ctx, ""); err == nil {
			t.Fatal("an empty identity must never be proven absent")
		} else if !strings.Contains(err.Error(), "identity is empty") {
			t.Fatalf("error = %v, want the empty identity refusal", err)
		}
	})

	t.Run("the clear falls back to the reserved container name", func(t *testing.T) {
		t.Parallel()
		fake := newReadyFake(t, policy)
		runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
		if err != nil {
			t.Fatalf("newContainerRuntime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.slot.Close() })
		runtime.daemonID = testContainerDaemonID
		prior := testContainerReservation()
		if err := runtime.slot.Reserve(prior); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if err := runtime.clearProvenReservationLocked(ctx, ""); err != nil {
			t.Fatalf("clearProvenReservationLocked: %v", err)
		}
		if _, ok := runtime.slot.Reservation(); ok {
			t.Fatal("the proven reservation must be cleared")
		}
	})

	t.Run("an attach client that exits only after the kill is reaped", func(t *testing.T) {
		t.Parallel()
		process := &killThenExitProcess{killed: make(chan struct{})}
		canceled := false
		runtime := &ContainerRuntime{
			containerID:       testContainerID,
			attach:            process,
			attachDone:        make(chan error, 1),
			attachCancel:      func() { canceled = true },
			attachReapTimeout: time.Millisecond,
			attachKillTimeout: time.Second,
		}
		go func() { runtime.attachDone <- process.Wait() }()
		if err := runtime.reapAttachLocked(); err != nil {
			t.Fatalf("reapAttachLocked: %v", err)
		}
		if !canceled {
			t.Fatal("the unresponsive attach context must be canceled")
		}
		if runtime.attach != nil || runtime.attachDone != nil {
			t.Fatal("the killed attach client must be reaped and released")
		}
	})

	t.Run("the default host resolves the real docker tool and home", func(t *testing.T) {
		t.Parallel()
		host := defaultContainerHost()
		toolPath, err := host.dockerPath()
		if err != nil {
			// Only a lookup that found no Docker at all is acceptable here:
			// the test must never run Docker, and a different lookup failure
			// would make the production seam lie about its outcome.
			if !errors.Is(err, exec.ErrNotFound) {
				t.Fatalf("default docker lookup returned an unrecognized error: %v", err)
			}
		} else {
			if !filepath.IsAbs(toolPath) {
				t.Fatalf("default docker tool path = %q, want an absolute path", toolPath)
			}
			info, statErr := os.Stat(toolPath)
			if statErr != nil {
				t.Fatalf("stat the resolved docker tool %q: %v", toolPath, statErr)
			}
			if info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("resolved docker tool %q is not executable (mode %04o)", toolPath, info.Mode().Perm())
			}
		}
		home, err := (&containerHost{}).homeDirectory()
		if err != nil {
			t.Fatalf("homeDirectory fallback: %v", err)
		}
		if home == "" {
			t.Fatal("the home fallback must resolve a directory")
		}
	})
}

// TestContainerRuntimeOpenRefusesAnInvalidReservationRunID proves the admission
// path still refuses a corrupt private run identity at the final reservation
// validation, before Docker create can run.
func TestContainerRuntimeOpenRefusesAnInvalidReservationRunID(t *testing.T) {
	t.Parallel()
	fake := newReadyFake(t, executionTestPolicy())
	runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
	if err != nil {
		t.Fatalf("newContainerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.slot.Close() })
	// Same length as a valid run ID but not hex, so only the final reservation
	// validation can notice the corruption.
	runtime.runID = IDPrefix + strings.Repeat("z", 32)
	if err := runtime.open(context.Background()); err == nil {
		t.Fatal("a corrupt private run ID must refuse the open")
	} else if !strings.Contains(err.Error(), "run ID") {
		t.Fatalf("error = %v, want the invalid reservation run ID named", err)
	}
	if fake.createCount() != 0 {
		t.Fatal("a refused reservation must block before create")
	}
}

// TestContainerRuntimeNewReservationRefusesAnInvalidDaemonIdentity proves the
// reservation's final validation refuses a daemon identity that the bounded
// Docker Engine pattern does not accept.
func TestContainerRuntimeNewReservationRefusesAnInvalidDaemonIdentity(t *testing.T) {
	t.Parallel()
	fake := newReadyFake(t, executionTestPolicy())
	runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
	if err != nil {
		t.Fatalf("newContainerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.slot.Close() })
	runtime.daemonID = "not a bounded engine id"
	if _, err := runtime.newReservation(); err == nil {
		t.Fatal("an unbounded Docker Engine identity must refuse the reservation")
	} else if !strings.Contains(err.Error(), "Docker Engine ID") {
		t.Fatalf("error = %v, want the invalid Docker Engine identity named", err)
	}
}

// TestContainerRuntimeCreateArgsFloorAZeroLifetime proves the create arguments
// never pass PID1 a non-positive lifetime, while the exported constructor still
// refuses a non-positive lifetime before this helper is reached.
func TestContainerRuntimeCreateArgsFloorAZeroLifetime(t *testing.T) {
	t.Parallel()
	runtime := &ContainerRuntime{
		policy:            executionTestPolicy(),
		ownerUser:         testContainerRuntimeOwner,
		imageID:           testContainerRuntimeImageID,
		containerLifetime: 0,
	}
	args := runtime.containerCreateArgs(testContainerReservation())
	if got := args[len(args)-1]; got != "1s" {
		t.Fatalf("lifetime argument = %q, want the one-second floor", got)
	}
}

// TestContainerRuntimeRecoveryRefusesAReservationWithNoIdentity proves the
// recovery guard refuses a reservation whose daemon matches but which records
// neither a container ID nor a name, without inspecting or removing anything.
func TestContainerRuntimeRecoveryRefusesAReservationWithNoIdentity(t *testing.T) {
	t.Parallel()
	fake := newReadyFake(t, executionTestPolicy())
	runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
	if err != nil {
		t.Fatalf("newContainerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.slot.Close() })
	runtime.daemonID = testContainerDaemonID
	// Corrupt the in-memory authority directly. This is an internal defensive
	// guard test only: a truncated durable record would be rejected by the
	// reservation parser in loadReservation before recovery ever sees it, so
	// this fixture proves the recovery guard itself refuses a daemon-matched
	// record with neither a container ID nor a name.
	runtime.slot.mutex.Lock()
	runtime.slot.reservation = &ContainerReservation{
		RunID:    testContainerRunID,
		DaemonID: testContainerDaemonID,
	}
	runtime.slot.mutex.Unlock()
	if err := runtime.recoverPriorReservation(context.Background()); err == nil {
		t.Fatal("a reservation with no identity must be refused")
	} else if !strings.Contains(err.Error(), "carries no identity") {
		t.Fatalf("error = %v, want the missing identity named", err)
	}
	if fake.callCount("inspect") != 0 || fake.callCount("rm") != 0 {
		t.Fatal("a reservation with no identity must not inspect or remove a container")
	}
}

// TestContainerRuntimeCloseReportsAFailedSlotRelease proves a slot whose owned
// descriptor can no longer be unlocked or closed is reported through cleanup,
// and the failed result is retained for a repeated Close.
func TestContainerRuntimeCloseReportsAFailedSlotRelease(t *testing.T) {
	t.Parallel()
	slot := openTestContainerSlot(t, testContainerSlotStateDir(t))
	runtime := &ContainerRuntime{slot: slot}
	// Close only this slot's owned descriptor and leave slot.closed false, so
	// the release observes both a failed unlock and a failed close.
	if err := slot.file.Close(); err != nil {
		t.Fatalf("close the slot's owned descriptor: %v", err)
	}
	err := runtime.Close()
	if err == nil {
		t.Fatal("a failed slot release must be reported")
	}
	if !strings.Contains(err.Error(), "release the container slot") {
		t.Fatalf("error = %v, want the failed slot release named", err)
	}
	if repeated := runtime.Close(); !errors.Is(repeated, err) {
		t.Fatalf("repeated Close = %v, want the retained failed result %v", repeated, err)
	}
}

// TestContainerRuntimeAttachContainerReportsAClosedParentCopy proves the
// attach seam reports a close failure of the parent's copy of the stdin read
// end while still owning the attached client for bounded cleanup.
func TestContainerRuntimeAttachContainerReportsAClosedParentCopy(t *testing.T) {
	t.Parallel()
	fake := newReadyFake(t, executionTestPolicy())
	runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
	if err != nil {
		t.Fatalf("newContainerRuntime: %v", err)
	}
	runtime.containerID = testContainerID
	t.Cleanup(func() { _ = runtime.Close() })
	fake.attachFactory = func(ctx context.Context, readEnd *os.File) (containerAttachProcess, error) {
		// Model a child that inherits the read end and then closes the supplied
		// descriptor before the controller closes its own copy.
		process := newTestAttachProcess(ctx, readEnd)
		if err := readEnd.Close(); err != nil {
			return nil, fmt.Errorf("close the supplied read end: %w", err)
		}
		return process, nil
	}
	attachErr := runtime.attachContainer()
	if attachErr == nil {
		t.Fatal("a closed parent copy of the attach stdin must be reported")
	}
	if !strings.Contains(attachErr.Error(), "close the parent copy of the container attach stdin") {
		t.Fatalf("error = %v, want the failed parent-copy close named", attachErr)
	}
	if runtime.attach == nil {
		t.Fatal("the attached client must still be owned for bounded cleanup")
	}
	if cleanupErr := runtime.Close(); cleanupErr != nil {
		t.Fatalf("bounded cleanup of the attached client: %v", cleanupErr)
	}
}

// TestContainerRuntimeAttachRefusesWhenThePipeCannotOpen proves attachContainer
// reports a stdin-pipe creation failure through its own instance seam and never
// retains half-built attach state. Production keeps the exact os.Pipe.
func TestContainerRuntimeAttachRefusesWhenThePipeCannotOpen(t *testing.T) {
	t.Parallel()
	pipeErr := errors.New("descriptor limit reached")
	runtime := &ContainerRuntime{
		openPipe: func() (*os.File, *os.File, error) {
			return nil, nil, pipeErr
		},
	}
	err := runtime.attachContainer()
	if err == nil {
		t.Fatal("a failed stdin pipe must be reported")
	}
	if !strings.Contains(err.Error(), "create the container attach stdin pipe") {
		t.Fatalf("error = %v, want the failed attach pipe named", err)
	}
	if !errors.Is(err, pipeErr) {
		t.Fatalf("error = %v, want the pipe failure wrapped", err)
	}
	if runtime.writer != nil || runtime.attach != nil || runtime.attachDone != nil {
		t.Fatal("a failed attach pipe must not retain attach state")
	}
}

// TestContainerRuntimeAttachUsesTheRealPipeByDefault proves an instance with no
// injected pipe factory falls back to the exact os.Pipe and completes attach.
func TestContainerRuntimeAttachUsesTheRealPipeByDefault(t *testing.T) {
	t.Parallel()
	fake := newReadyFake(t, executionTestPolicy())
	runtime, err := newContainerRuntime(testContainerRuntimeOptions(testContainerSlotStateDir(t), fake))
	if err != nil {
		t.Fatalf("newContainerRuntime: %v", err)
	}
	runtime.openPipe = nil
	runtime.containerID = testContainerID
	if err := runtime.attachContainer(); err != nil {
		_ = runtime.Close()
		t.Fatalf("attachContainer with the default pipe: %v", err)
	}
	if runtime.writer == nil || runtime.attach == nil {
		_ = runtime.Close()
		t.Fatal("a successful attach must own the writer and the attached client")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close the attached runtime: %v", err)
	}
}
