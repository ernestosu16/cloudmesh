package mega

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/storage"
)

// Con el transporte cerrado, el sondeo de eventos termina su goroutine y el
// resto de peticiones recibe errClosed.
func TestClosedTransportEndsPolling(t *testing.T) {
	var closed atomic.Bool
	closed.Store(true)
	c := &http.Client{Timeout: time.Second, Transport: &closableTransport{closed: &closed}}

	if _, err := c.Post("https://g.api.mega.co.nz/cs?id=1", "application/json", nil); !errors.Is(err, errClosed) {
		t.Fatalf("cs: err = %v, se esperaba errClosed", err)
	}

	done := make(chan bool)
	go func() {
		returned := false
		defer func() { done <- returned }()
		c.Post("https://g.api.mega.co.nz/sc?sn=x&sid=y", "application/xml", nil)
		returned = true
	}()
	select {
	case returned := <-done:
		if returned {
			t.Fatal("el sondeo volvió en vez de terminar la goroutine")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("la goroutine de sondeo no terminó")
	}
}

// Solo el estado HTTP 509 cuenta como límite de transferencia; un corte de red
// cuya URL lleva «509» no aparta la cuenta.
func TestDownloadErrorTransferQuota(t *testing.T) {
	quota := errors.New("Http Status: 509 Bandwidth Limit Exceeded")
	if err := downloadError(quota); !errors.Is(err, storage.ErrTransferQuota) {
		t.Fatalf("509: err = %v", err)
	}
	net := errors.New(`Get "https://gfs270n509.userstorage.mega.co.nz/dl/x/1509-2048": dial tcp: lookup gfs270n509.userstorage.mega.co.nz: no such host`)
	if err := downloadError(net); errors.Is(err, storage.ErrTransferQuota) {
		t.Fatalf("corte de red tomado por límite: %v", err)
	}
}
