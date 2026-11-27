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
	"os"
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

// writeJSON writes a JSON response. A successful one gets the ETag of its
// body; an error body is no representation of the resource and gets none.
func writeJSON(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", odata.ContentType)
	h.Set("OData-Version", odata.Version)
	if status < 300 {
		h.Set("ETag", etag(body))
	}
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

// StaticXML returns a handler that serves body, a fixed XML document such as
// the service metadata (EDMX). Its ETag is computed once.
func StaticXML(body string) http.HandlerFunc {
	return static("application/xml; charset=utf-8", body)
}

// StaticJSON returns a handler that serves body, a fixed JSON document such
// as the OData service document. Its ETag is computed once.
func StaticJSON(body string) http.HandlerFunc {
	return static(odata.ContentType, body)
}

func static(contentType, body string) http.HandlerFunc {
	data := []byte(body)
	tag := etag(data)
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("OData-Version", odata.Version)
		h.Set("ETag", tag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

// WriteCreated writes r as a 201 Created response for a resource a POST
// created at location.
func WriteCreated(w http.ResponseWriter, location string, r Resource) {
	w.Header().Set("Location", location)
	writeJSON(w, http.StatusCreated, r.Bytes())
}

// NoContent writes a 204 No Content response, the standard result of a Redfish
// action that completes synchronously without a response body.
func NoContent(w http.ResponseWriter) {
	w.Header().Set("OData-Version", odata.Version)
	w.WriteHeader(http.StatusNoContent)
}

// ActionResult is the outcome of an action a provider performed. A nil
// *ActionResult reports an action that completed without a response body.
type ActionResult struct {
	// TaskMonitor, when set, reports the action as accepted for asynchronous
	// processing: the response is 202 Accepted with a Location header pointing
	// at the task monitor URI.
	TaskMonitor string
	// Body is the response body: the result of a completed action, or the Task
	// representing an accepted one. It may be nil.
	Body Resource
}

// WriteActionResult writes the response to an action: 204 No Content for a nil
// result, 202 Accepted for an asynchronous one, otherwise 200 OK with the body.
func WriteActionResult(w http.ResponseWriter, res *ActionResult) {
	switch {
	case res == nil || (res.TaskMonitor == "" && res.Body == nil):
		NoContent(w)
	case res.TaskMonitor != "":
		w.Header().Set("Location", res.TaskMonitor)
		if res.Body == nil {
			w.Header().Set("OData-Version", odata.Version)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusAccepted, res.Body.Bytes())
	default:
		writeJSON(w, http.StatusOK, res.Body.Bytes())
	}
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

// NotWritable returns the 400 error for a PATCH of a read-only property.
func NotWritable(property string) *Error {
	return &Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgPropertyNotWrite,
		Message:   "The property " + property + " is a read-only property and cannot be assigned a value.",
	}
}

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
// and the resource's current body. A missing header or "*" always matches;
// otherwise one of the entity tags must equal the current one, and a weak tag
// never does (RFC 9110, section 13.1.1).
func CheckIfMatch(r *http.Request, currentBody []byte) bool {
	im := strings.TrimSpace(r.Header.Get("If-Match"))
	if im == "" || im == "*" {
		return true
	}
	cur := etag(currentBody)
	for tag := range strings.SplitSeq(im, ",") {
		if strings.TrimSpace(tag) == cur {
			return true
		}
	}
	return false
}

// ReadBody reads the body of r, which may hold up to 1 MiB. Its errors are
// those of ReadError.
func ReadBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err == nil && len(body) > maxBodyBytes {
		err = &http.MaxBytesError{Limit: maxBodyBytes}
	}
	if err != nil {
		return nil, ReadError(err)
	}
	return body, nil
}

// ReadError returns the error that answers a failure to read a request
// body: 413 when the body exceeds a limit (an *http.MaxBytesError), 408 when
// the client stopped sending it (see BodyIdleTimeout), otherwise 400.
func ReadError(err error) *Error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return &Error{
			Status:    http.StatusRequestEntityTooLarge,
			MessageID: odata.MsgGeneralError,
			Message:   "The request body exceeds the size the service accepts.",
		}
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return &Error{
			Status:    http.StatusRequestTimeout,
			MessageID: odata.MsgGeneralError,
			Message:   "The client stopped sending the request body before it was complete.",
		}
	}
	return &Error{
		Status:    http.StatusBadRequest,
		MessageID: odata.MsgUnrecognizedBody,
		Message:   "The request body could not be read.",
	}
}

func severityFor(status int) string {
	if status >= 500 {
		return "Critical"
	}
	return "Warning"
}
