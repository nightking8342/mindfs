package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agenttypes "mindfs/server/internal/agent/types"
)

// A hung session open (subprocess spawn / protocol handshake that never
// answers) used to hold the per-runtime-group lock forever, which stalled every
// later GetOrCreate, Close and idle release for that agent — the whole server
// stopped serving sessions with nothing logged. awaitOpenSession must bound
// that wait and surface a timeout error instead.
func TestAwaitOpenSessionTimesOutWhenOpenHangs(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	sess, err := awaitOpenSession(context.Background(), 50*time.Millisecond, agenttypes.OpenSessionInput{
		AgentName:  "claude",
		SessionKey: "hung",
	}, func() (agenttypes.Session, error) {
		<-release
		return nil, errors.New("never reached")
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if sess != nil {
		t.Fatalf("expected nil session, got %#v", sess)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited too long: %s", elapsed)
	}
}

// The abandoned open must not leak the session it eventually produces: once the
// caller has given up, anything that comes up later is closed.
func TestAwaitOpenSessionClosesAbandonedSession(t *testing.T) {
	sess := &idleReleaseTestSession{}
	release := make(chan struct{})

	_, err := awaitOpenSession(context.Background(), 30*time.Millisecond, agenttypes.OpenSessionInput{
		AgentName:  "claude",
		SessionKey: "late",
	}, func() (agenttypes.Session, error) {
		<-release
		return sess, nil
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}

	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sess.closeCount == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("abandoned session was not closed (closeCount=%d)", sess.closeCount)
}

// Cancelling the caller's context must also give up, without waiting for the
// full timeout.
func TestAwaitOpenSessionReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	defer close(release)

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := awaitOpenSession(ctx, time.Hour, agenttypes.OpenSessionInput{
		AgentName:  "claude",
		SessionKey: "cancelled",
	}, func() (agenttypes.Session, error) {
		<-release
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited too long: %s", elapsed)
	}
}

// The normal path is unchanged: a fast open returns its session untouched.
func TestAwaitOpenSessionReturnsSessionOnSuccess(t *testing.T) {
	want := &idleReleaseTestSession{}
	got, err := awaitOpenSession(context.Background(), time.Second, agenttypes.OpenSessionInput{
		AgentName:  "claude",
		SessionKey: "fast",
	}, func() (agenttypes.Session, error) {
		return want, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if want.closeCount != 0 {
		t.Fatalf("successful open must not be closed, closeCount=%d", want.closeCount)
	}
}

// A failing open propagates its own error rather than a timeout.
func TestAwaitOpenSessionPropagatesOpenError(t *testing.T) {
	sentinel := errors.New("spawn failed")
	_, err := awaitOpenSession(context.Background(), time.Second, agenttypes.OpenSessionInput{
		AgentName:  "claude",
		SessionKey: "fails",
	}, func() (agenttypes.Session, error) {
		return nil, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}
