package server

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// BodyIdleTimeout wraps h so that reading a request body fails once the client
// has sent nothing for d. Unlike http.Server's ReadTimeout it limits
// inactivity, not the whole request: an update image pushed over a slow link
// may take minutes, but a client that stops sending in the middle no longer
// holds the request, and with it for example the update service, forever.
//
// The deadline is armed for each read of the body only, so time the handler
// spends between reads does not count. Over TLS a read completes with a whole
// TLS record of up to 16 KiB, so a link must carry that much within d; with
// d of a minute that is about 270 bytes per second. Set http.Server's
// ReadHeaderTimeout and IdleTimeout as well: this handler only sees requests
// whose headers have arrived.
//
// The deadline is set through http.ResponseController, so middleware between
// the server and this handler that wraps the ResponseWriter must implement
// Unwrap. Otherwise reading the body fails with an error that wraps
// http.ErrNotSupported rather than going without a limit.
func BodyIdleTimeout(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			h.ServeHTTP(w, r)
			return
		}
		// Wrap the body of a copy: net/http looks at the body of its own
		// request, for example to reject an "Expect: 100-continue" request
		// that the handler answers without reading the body.
		r = r.WithContext(r.Context())
		r.Body = &idleBody{ReadCloser: r.Body, rc: http.NewResponseController(w), d: d}
		h.ServeHTTP(w, r)
	})
}

type idleBody struct {
	io.ReadCloser
	rc *http.ResponseController
	d  time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	if err := b.rc.SetReadDeadline(time.Now().Add(b.d)); err != nil {
		return 0, fmt.Errorf("server: cannot limit the inactivity of the request body: %w", err)
	}
	n, err := b.ReadCloser.Read(p)
	// Disarmed until the next read: an HTTP/2 stream deadline that expires
	// while the handler is busy closes the body however much the client sends.
	_ = b.rc.SetReadDeadline(time.Time{})
	return n, err
}
