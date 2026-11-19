package server

import (
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
// The read deadline moves on with every read and is cleared at the end of the
// body, as the server keeps reading the connection to notice a client going
// away. Set http.Server's ReadHeaderTimeout and IdleTimeout as well: this
// handler only sees requests whose headers have arrived.
func BodyIdleTimeout(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	_ = b.rc.SetReadDeadline(time.Now().Add(b.d))
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}
