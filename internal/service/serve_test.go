package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// gatedServer serves requests that wait for release before answering.
func gatedServer(t *testing.T) (*http.Server, net.Listener, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, "done")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return srv, ln, release
}

func TestServeDrainsTheRequestsInFlight(t *testing.T) {
	srv, ln, release := gatedServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	stopped := false
	go func() { served <- Serve(ctx, srv, ln, time.Second, func() { stopped = true }) }()

	answered := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			answered <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		answered <- string(body)
	}()
	time.Sleep(50 * time.Millisecond) // the request is in flight
	cancel()
	time.Sleep(50 * time.Millisecond) // the drain has started
	close(release)
	if body := <-answered; body != "done" {
		t.Errorf("in-flight request answered %q", body)
	}
	if err := <-served; err != nil || !stopped {
		t.Errorf("Serve returned %v, onStop called %v", err, stopped)
	}
}

func TestServeGivesUpAfterTheDrain(t *testing.T) {
	srv, ln, release := gatedServer(t)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, srv, ln, 100*time.Millisecond, nil) }()
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-served:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Serve returned %v, want the drain deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the drain")
	}
}

func TestServeReportsAServerThatStopsByItself(t *testing.T) {
	srv, ln, _ := gatedServer(t)
	ln.Close()
	err := Serve(context.Background(), srv, ln, time.Second, nil)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Errorf("Serve returned %v on a closed listener", err)
	}
}
