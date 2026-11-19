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
		WriteError(w, http.StatusBadRequest, "Base.1.0.GeneralError", err.Error())
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
	// The client, which still has the connection open, gets the response:
	// net/http does not wait for the rest of the body before sending it.
	if line := readStatus(t, conn); !strings.HasPrefix(line, "HTTP/1.1 400") {
		t.Errorf("response = %q, want 400", line)
	}
}

// readStatus reads the status line of the response on conn, or fails after
// five seconds.
func readStatus(t *testing.T, conn net.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Errorf("reading the response: %v", err)
	}
	return line
}

// TestBodyIdleTimeoutUnreadRest answers after reading only the start of a
// body whose client then stalls. net/http drains the rest before it sends
// the response, and the limit applies to that as well.
func TestBodyIdleTimeoutUnreadRest(t *testing.T) {
	h := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 10)
		if _, err := io.ReadFull(r.Body, buf); err != nil {
			t.Errorf("read: %v", err)
		}
		WriteError(w, http.StatusRequestEntityTooLarge, "Base.1.0.GeneralError", "too large")
	}), 100*time.Millisecond)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n0123456789abcdef")
	if line := readStatus(t, conn); !strings.HasPrefix(line, "HTTP/1.1 413") {
		t.Errorf("response = %q, want 413", line)
	}
}

// TestBodyIdleTimeoutMultipartFiles parses a multipart form into files: they
// are removed after the request, as net/http removes those of its own request.
func TestBodyIdleTimeoutMultipartFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	h := BodyIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		if files, _ := os.ReadDir(tmp); len(files) == 0 {
			t.Error("no file for the uploaded part")
		}
	}), time.Minute)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	body := "--b\r\nContent-Disposition: form-data; name=\"f\"; filename=\"f.bin\"\r\n\r\n" +
		strings.Repeat("x", 32<<10) + "\r\n--b--\r\n"
	resp, err := http.Post(srv.URL, "multipart/form-data; boundary=b", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if files, _ := os.ReadDir(tmp); len(files) != 0 {
		t.Errorf("files left after the request: %v", files)
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
	if line := readStatus(t, conn); !strings.HasPrefix(line, "HTTP/1.1 413") {
		t.Errorf("response = %q, want 413 at once", line)
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
