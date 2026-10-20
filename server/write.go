// Package server provides the small HTTP runtime that generated Redfish
// handlers use: writing JSON resources and collections with the correct
// headers, ETags, and standard error bodies. It uses only net/http and the
// odata package; routing is left to net/http's ServeMux.
package server

import (
	"errors"
	"hash/fnv"
	"io"
	"net/http"
	"strconv"
	"strings"

	"src.kyanite.computer/redfish/odata"
)

// maxBodyBytes bounds the size of a request body the server will read.
const maxBodyBytes = 1 << 20

// Resource is implemented by generated DTOs and the odata.Collection type: any
// value that can render itself as a Redfish JSON document.
type Resource interface {
	Bytes() []byte
}

// etag computes a strong ETag from the response body. A strong validator is
// required for Redfish If-Match concurrency control; the FNV hash is exact for
// identical bodies and avoids pulling in a cryptographic hash.
func etag(body []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(body)
	return `"` + strconv.FormatUint(h.Sum64(), 16) + `"`
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", odata.ContentType)
	h.Set("OData-Version", odata.Version)
	h.Set("ETag", etag(body))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// WriteResource writes r as a 200 OK Redfish JSON response.
func WriteResource(w http.ResponseWriter, r Resource) {
	writeJSON(w, http.StatusOK, r.Bytes())
}

// WriteJSON writes a pre-encoded JSON body as a 200 OK Redfish response, used
// for fixed documents such as the OData service document.
func WriteJSON(w http.ResponseWriter, body []byte) {
	writeJSON(w, http.StatusOK, body)
}

// WriteXML writes the service metadata document (EDMX) as a 200 OK response.
func WriteXML(w http.ResponseWriter, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/xml; charset=utf-8")
	h.Set("OData-Version", odata.Version)
	h.Set("ETag", etag(body))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// NoContent writes a 204 No Content response, the standard result of a Redfish
// action that completes synchronously without a response body.
func NoContent(w http.ResponseWriter) {
	w.Header().Set("OData-Version", odata.Version)
	w.WriteHeader(http.StatusNoContent)
}

// WriteError writes a Redfish error response with the given HTTP status, base
// message code, and human-readable message.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	body := odata.Error{
		Code:    code,
		Message: message,
		ExtendedInfo: []odata.ExtendedInfo{
			{MessageID: code, Message: message, Severity: severityFor(status)},
		},
	}.Bytes()
	writeJSON(w, status, body)
}

// Error is an error a provider returns to choose the HTTP status and Redfish
// message of the response. Errors that are not (and do not wrap) an *Error are
// reported as 500 Internal Server Error.
type Error struct {
	Status    int    // HTTP status code
	MessageID string // Redfish message identifier, e.g. odata.MsgGeneralError
	Message   string // human-readable message
}

func (e *Error) Error() string { return e.Message }

// WriteErr writes err as a Redfish error response, using the status and
// message of an *Error in err's chain or a generic 500 otherwise.
func WriteErr(w http.ResponseWriter, err error) {
	if e, ok := errors.AsType[*Error](err); ok {
		WriteError(w, e.Status, e.MessageID, e.Message)
		return
	}
	Internal(w)
}

// NotFound writes a standard Redfish 404 for the requested URI.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, http.StatusNotFound, odata.MsgResourceNotFound,
		"The resource at URI '"+r.URL.Path+"' was not found.")
}

// Internal writes a standard Redfish 500.
func Internal(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, odata.MsgInternalError,
		"The server encountered an internal error.")
}

// BadRequest writes a Redfish 400 with the given base message code.
func BadRequest(w http.ResponseWriter, code, message string) {
	WriteError(w, http.StatusBadRequest, code, message)
}

// PreconditionFailed writes a Redfish 412, used when an If-Match header does not
// match the resource's current ETag.
func PreconditionFailed(w http.ResponseWriter) {
	WriteError(w, http.StatusPreconditionFailed, odata.MsgPreconditionFail,
		"The If-Match header did not match the current ETag of the resource.")
}

// CheckIfMatch reports whether a request may proceed given its If-Match header
// and the resource's current body. A missing header or "*" always matches.
func CheckIfMatch(r *http.Request, currentBody []byte) bool {
	im := strings.TrimSpace(r.Header.Get("If-Match"))
	if im == "" || im == "*" {
		return true
	}
	cur := etag(currentBody)
	for tag := range strings.SplitSeq(im, ",") {
		tag = strings.TrimSpace(tag)
		tag = strings.TrimPrefix(tag, "W/") // accept weak form defensively
		if tag == cur {
			return true
		}
	}
	return false
}

// ReadBody reads a request body up to a fixed size limit.
func ReadBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
}

func severityFor(status int) string {
	if status >= 500 {
		return "Critical"
	}
	return "Warning"
}
