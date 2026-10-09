package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPinnedMigrationClientsCloseConnectionsAfterEachRequest(t *testing.T) {
	var opened, closed atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed, http.StateHijacked:
			closed.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().Raw)
	fingerprint := hex.EncodeToString(sum[:])

	clients := map[string]func() (*http.Client, error){
		"control":   func() (*http.Client, error) { return pinnedMigrationClient(fingerprint, 5*time.Second) },
		"streaming": func() (*http.Client, error) { return pinnedMigrationStreamingClient(fingerprint) },
	}
	const requests = 3
	for name, build := range clients {
		for i := 0; i < requests; i++ {
			client, err := build()
			if err != nil {
				t.Fatalf("%s client: %v", name, err)
			}
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("%s request: %v", name, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	want := int64(len(clients) * requests)
	deadline := time.Now().Add(3 * time.Second)
	for closed.Load() < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if opened.Load() != want || closed.Load() != want {
		t.Fatalf("connections opened=%d closed=%d, want both %d", opened.Load(), closed.Load(), want)
	}
}
