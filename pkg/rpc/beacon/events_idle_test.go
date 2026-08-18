package beacon

// Tests for NM-04: the SSE event stream had no read/idle deadline, so a
// stalled-but-open connection (bytes stop arriving, no TCP-level failure)
// blocked the topic goroutine's read forever, silently dropping p2p reveals
// for as long as the stall lasted. runIdleWatchdog cancels the connection's
// own context when no read activity is reported within sseIdleTimeout,
// which unblocks a Read() already parked in processStream.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunIdleWatchdog_CancelsAfterNoActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	activity := make(chan struct{})
	done := make(chan struct{})

	var idleFired bool

	go runIdleWatchdog(ctx, cancel, activity, done, 30*time.Millisecond, func() {
		idleFired = true
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not fire within its idle timeout")
	}

	assert.True(t, idleFired, "onIdle callback must fire before cancelling")
	assert.Error(t, ctx.Err(), "watchdog must cancel the connection context on idle")
}

func TestRunIdleWatchdog_ActivityResetsTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	activity := make(chan struct{}, 1)
	done := make(chan struct{})

	go runIdleWatchdog(ctx, cancel, activity, done, 80*time.Millisecond, func() {})

	// Keep sending activity faster than the idle timeout for well past it:
	// the watchdog must never fire while activity keeps arriving.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case activity <- struct{}{}:
		case <-done:
			t.Fatal("watchdog cancelled despite continuous activity")
		}

		time.Sleep(20 * time.Millisecond)
	}

	require.NoError(t, ctx.Err(), "context must still be live after sustained activity")

	// Now stop sending activity: the watchdog must fire once the idle
	// timeout elapses with nothing arriving.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not fire after activity stopped")
	}
}

func TestRunIdleWatchdog_StopsOnOuterContextCancel(t *testing.T) {
	outerCtx, outerCancel := context.WithCancel(context.Background())
	connCtx, connCancel := context.WithCancel(outerCtx)

	defer connCancel()

	activity := make(chan struct{})
	done := make(chan struct{})

	go runIdleWatchdog(connCtx, connCancel, activity, done, time.Hour, func() {
		t.Error("onIdle must not fire: the outer context was cancelled, not an idle timeout")
	})

	outerCancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not exit when the outer context was cancelled")
	}
}

// sseTestServer runs an httptest server whose handler is invoked per request;
// it always sets the SSE headers and flushes them before calling body.
func sseTestServer(t *testing.T, body func(w http.ResponseWriter, flusher http.Flusher, done <-chan struct{})) *httptest.Server {
	t.Helper()

	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		flusher.Flush()

		body(w, flusher, done)
	}))

	// t.Cleanup runs LIFO: close(done) (registered last) must run BEFORE
	// srv.Close (registered first), so the still-blocked handler goroutine
	// above unblocks and returns before Close waits for it -- otherwise the
	// two deadlock against each other.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) })

	return srv
}

// TestConnectAndStreamTopic_StalledConnectionReconnects reproduces the exact
// NM-04 scenario end to end over a real HTTP connection: the server accepts
// the SSE request and then goes silent (no bytes, connection stays open) --
// no TCP-level failure ever occurs. Before the fix this blocked forever;
// connectAndStreamTopic must now return within roughly sseIdleTimeout.
func TestConnectAndStreamTopic_StalledConnectionReconnects(t *testing.T) {
	orig := sseIdleTimeout
	sseIdleTimeout = 100 * time.Millisecond

	defer func() { sseIdleTimeout = orig }()

	srv := sseTestServer(t, func(_ http.ResponseWriter, _ http.Flusher, done <-chan struct{}) {
		<-done // never write anything -- a stalled-but-open connection
	})

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	e := NewEventStream(&Client{baseURL: srv.URL, log: log})

	errCh := make(chan error, 1)

	go func() {
		errCh <- e.connectAndStreamTopic(context.Background(), "head")
	}()

	select {
	case err := <-errCh:
		require.Error(t, err, "a stalled connection must be treated as a failure so runTopicLoop reconnects")
	case <-time.After(2 * time.Second):
		t.Fatal("connectAndStreamTopic did not return -- NM-04 reproduced (stalled read blocks forever)")
	}
}

// TestConnectAndStreamTopic_ActiveConnectionSurvivesIdleWindow proves the
// fix does not misfire on a healthy connection: periodic SSE comment lines
// (the beacon-node keepalive convention), arriving faster than the idle
// timeout, must keep the connection open well past several idle windows.
func TestConnectAndStreamTopic_ActiveConnectionSurvivesIdleWindow(t *testing.T) {
	orig := sseIdleTimeout
	sseIdleTimeout = 80 * time.Millisecond

	defer func() { sseIdleTimeout = orig }()

	var pings int

	var mu sync.Mutex

	srv := sseTestServer(t, func(w http.ResponseWriter, flusher http.Flusher, done <-chan struct{}) {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_, _ = w.Write([]byte(": ping\n\n"))
				flusher.Flush()

				mu.Lock()
				pings++
				mu.Unlock()
			}
		}
	})

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	e := NewEventStream(&Client{baseURL: srv.URL, log: log})

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	err := e.connectAndStreamTopic(ctx, "head")

	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the connection must survive on its own (via keepalive pings) until the test's own deadline ends it, "+
			"not get killed early by the idle watchdog")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, pings, 4, "expected several keepalive pings to have been exchanged across multiple idle windows")
}
