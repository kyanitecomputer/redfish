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
// Time the handler spends between reads of the body does not count. Over TLS
// a read completes with a whole TLS record of up to 16 KiB, so a link must
// carry that much within d; with d of a minute that is about 270 bytes per
// second. The limit replaces http.Server's ReadTimeout for the body. Set
// ReadHeaderTimeout and IdleTimeout as well: this handler only sees requests
// whose headers have arrived.
//
// On HTTP/1 the limit also covers the rest of a body the handler does not
// read to its end, which net/http reads and discards before it responds.
// When the client has not sent that rest within d of the handler's last read
// (or of the start of the request), the response is sent and the connection
// closed after it.
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
		// that the handler answers without reading the body. It also removes
		// the files of a parsed multipart form only for its own request.
		rc := r.WithContext(r.Context())
		body := &idleBody{
			ReadCloser: r.Body,
			rc:         http.NewResponseController(w),
			d:          d,
			http2:      r.ProtoMajor == 2,
		}
		rc.Body = body
		if !body.http2 {
			// For a handler that answers without reading the body.
			_ = body.rc.SetReadDeadline(time.Now().Add(d))
		}
		defer func() {
			if f := rc.MultipartForm; f != nil && f != r.MultipartForm {
				_ = f.RemoveAll()
			}
		}()
		h.ServeHTTP(w, rc)
	})
}

type idleBody struct {
	io.ReadCloser
	rc    *http.ResponseController
	d     time.Duration
	http2 bool
}

// Read reads the body with a deadline of d. When the read fails, the
// deadline stays as it is, expired after a timeout, so that net/http does
// not wait for the rest of the body either when it drains it before the
// response. Otherwise, on HTTP/1, the deadline only matters while the
// connection is read; it stays armed, which also limits such a drain, and is
// cleared at the end of the body, after which the server reads the
// connection to notice a client going away. On HTTP/2 an expired deadline
// closes the body even while the handler is not reading, so it is cleared
// after every read.
func (b *idleBody) Read(p []byte) (int, error) {
	if err := b.rc.SetReadDeadline(time.Now().Add(b.d)); err != nil {
		return 0, fmt.Errorf("server: cannot limit the inactivity of the request body: %w", err)
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF || (err == nil && b.http2) {
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}
