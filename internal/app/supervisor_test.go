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

// Encerramento durante uma troca a quente das portas.

// dialable diz se algo aceita conexões na porta local dada.
func dialable(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func TestPortCommitAfterShutdownIsDiscarded(t *testing.T) {
	p := &port{name: "tráfego", key: "ports.traffic", handler: http.NotFoundHandler(), fatal: func(err error) { t.Error(err) }}
	cur, err := p.open(0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.commit(cur) {
		t.Fatal("a primeira troca deveria ser feita")
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
		t.Fatal("a troca não deveria ser feita depois de o encerramento começar")
	}
	if dialable(nextPort) {
		t.Fatalf("o listener descartado na porta %d deveria estar fechado", nextPort)
	}
	if p.cur != cur {
		t.Fatal("a porta deveria seguir no binding que o encerramento alcançou")
	}
}

func TestShutdownDuringPortSwitchLeavesNothingListening(t *testing.T) {
	for _, key := range []string{"traffic", "admin"} {
		t.Run(key, func(t *testing.T) {
			e := startAdmin(t, freePorts, adminRoutes(t))
			port := freePort(t)

			// O writer fica preso enquanto o PATCH chega e o encerramento
			// começa; o PATCH só aplica a troca depois disso.
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
			// Dá tempo de o PATCH chegar ao writer antes do encerramento.
			time.Sleep(200 * time.Millisecond)

			stopped := make(chan error, 1)
			go func() { stopped <- e.Shutdown(context.Background()) }()
			time.Sleep(100 * time.Millisecond)
			close(release)

			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("encerramento: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("o encerramento não concluiu")
			}
			<-patched
			if dialable(port) {
				t.Fatalf("depois do encerramento a porta nova %d não deveria aceitar conexões", port)
			}
		})
	}
}
