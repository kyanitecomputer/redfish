package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestBodyIdleTimeoutExpectContinue rejects a request with "Expect:
// 100-continue" without reading its body. The client sends the body only
// after "100 Continue", so the rejection must not wait for it.
func TestBodyIdleTimeoutExpectContinue(t *testing.T) {
	h := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusRequestEntityTooLarge, "Base.1.0.GeneralError", "too large")
	}), time.Minute)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1000000\r\nExpect: 100-continue\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "HTTP/1.1 413") {
		t.Errorf("response = %q, %v; want 413 at once", line, err)
	}
}

// TestBodyIdleTimeoutHTTP2Pause pauses the handler between two reads of a
// body for longer than the limit, over HTTP/2, while the client is sending.
func TestBodyIdleTimeoutHTTP2Pause(t *testing.T) {
	const d = 100 * time.Millisecond
	h := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 5)
		if _, err := io.ReadFull(r.Body, buf); err != nil {
			t.Errorf("first read: %v", err)
		}
		time.Sleep(3 * d)
		rest, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read after the pause: %v", err)
		}
		_, _ = w.Write(append(buf, rest...))
	}), d)
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	// The client keeps sending, a piece every d/2, while the handler pauses.
	pr, pw := io.Pipe()
	go func() {
		for _, piece := range []string{"01234", "56", "78", "9"} {
			_, _ = pw.Write([]byte(piece))
			time.Sleep(d / 2)
		}
		pw.Close()
	}()
	resp, err := srv.Client().Post(srv.URL, "text/plain", pr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 2 || string(body) != "0123456789" {
		t.Errorf("HTTP/%d response %q, want HTTP/2 and the whole body", resp.ProtoMajor, body)
	}
}

// opaqueWriter hides the ResponseWriter it wraps from http.ResponseController.
type opaqueWriter struct{ http.ResponseWriter }

// TestBodyIdleTimeoutUnsupported reads a body through a ResponseWriter that
// cannot set deadlines: the read fails instead of waiting without a limit.
func TestBodyIdleTimeoutUnsupported(t *testing.T) {
	got := make(chan error, 1)
	inner := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		got <- err
	}), time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(opaqueWriter{w}, r)
	}))
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nsome")
	select {
	case err := <-got:
		if !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("err = %v, want http.ErrNotSupported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading the body waits without a limit")
	}
}
