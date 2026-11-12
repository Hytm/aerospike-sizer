package service

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Serve runs the HTTP server on the listener until ctx is done, then stops accepting, calls
// onStop and finishes the requests in flight for at most drain. It returns nil after a clean
// drain, context.DeadlineExceeded when the drain ran out, and the server's own error when it
// stopped by itself.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration, onStop func()) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	if onStop != nil {
		onStop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
