package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestBodyIdleTimeout(t *testing.T) {
	type result struct {
		n   int
		err error
	}
	got := make(chan result, 1)
	h := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		got <- result{int(n), err}
	}), 100*time.Millisecond)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")
	// Data that keeps coming, even over more than the limit in total, is fine.
	for range 5 {
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(conn, "0123456789")
	}
	select {
	case r := <-got:
		if r.n != 50 || !errors.Is(r.err, os.ErrDeadlineExceeded) {
			t.Errorf("stalled body: read %d bytes, err %v; want 50 and a deadline error", r.n, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a stalled body did not time out")
	}
}

func TestBodyIdleTimeoutComplete(t *testing.T) {
	h := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		_, _ = w.Write(b)
	}), time.Second)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d", resp.StatusCode)
	}
}
