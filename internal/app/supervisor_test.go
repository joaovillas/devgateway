package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Shutting down in the middle of a live port swap.

// dialable reports whether anything accepts connections on the given local
// port.
func dialable(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func TestPortCommitAfterShutdownIsDiscarded(t *testing.T) {
	p := &port{name: "traffic", key: "ports.traffic", handler: http.NotFoundHandler(), fatal: func(err error) { t.Error(err) }}
	cur, err := p.open(0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.commit(cur) {
		t.Fatal("the first swap should go through")
	}
	next, err := p.open(0)
	if err != nil {
		t.Fatal(err)
	}
	nextPort := next.ln.Addr().(*net.TCPAddr).Port
	if err := p.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.commit(next) {
		t.Fatal("the swap should not go through once the shutdown has begun")
	}
	if dialable(nextPort) {
		t.Fatalf("the listener discarded on port %d should be closed", nextPort)
	}
	if p.cur != cur {
		t.Fatal("the port should stay on the binding the shutdown reached")
	}
}

func TestShutdownDuringPortSwitchLeavesNothingListening(t *testing.T) {
	for _, key := range []string{"traffic", "admin"} {
		t.Run(key, func(t *testing.T) {
			e := startAdmin(t, freePorts, adminRoutes(t))
			port := freePort(t)

			// The writer stays held while the PATCH arrives and the shutdown
			// begins; the PATCH only applies the swap after that.
			held, release := make(chan struct{}), make(chan struct{})
			go e.Writer.Exclusive(func() error {
				close(held)
				<-release
				return nil
			})
			<-held
			patched := make(chan struct{})
			go func() {
				defer close(patched)
				req, _ := http.NewRequest("PATCH", e.api+"/settings",
					strings.NewReader(fmt.Sprintf(`{"ports":{%q:%d}}`, key, port)))
				req.Header.Set("Content-Type", "application/merge-patch+json")
				if res, err := http.DefaultClient.Do(req); err == nil {
					res.Body.Close()
				}
			}()
			// Give the PATCH time to reach the writer before the shutdown.
			time.Sleep(200 * time.Millisecond)

			stopped := make(chan error, 1)
			go func() { stopped <- e.Shutdown(context.Background()) }()
			time.Sleep(100 * time.Millisecond)
			close(release)

			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the shutdown did not complete")
			}
			<-patched
			if dialable(port) {
				t.Fatalf("after the shutdown the new port %d should not accept connections", port)
			}
		})
	}
}
