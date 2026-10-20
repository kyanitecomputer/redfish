package server

import (
	"net/http"
	"strings"
)

// Params gives a provider the matched route of a request: the Redfish URI
// template it was registered under and the values of its path parameters.
// Because a resource type may be served under several URIs (for example
// FirmwareInventory and SoftwareInventory members), URI tells the provider
// which one was requested.
type Params struct {
	URI string // matched URI template, e.g. "/redfish/v1/Chassis/{ChassisId}"
	req *http.Request
}

// Get returns the value of the named path parameter, or "" if the matched URI
// does not declare it.
func (p Params) Get(name string) string {
	if p.req == nil {
		return ""
	}
	return p.req.PathValue(name)
}

// Path returns the request path without a trailing slash, the @odata.id of the
// requested resource.
func (p Params) Path() string {
	if p.req == nil {
		return ""
	}
	path := p.req.URL.Path
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

// HandlerFunc handles a request matched by Handle.
type HandlerFunc func(w http.ResponseWriter, r *http.Request, p Params)

// Handle registers h on mux for method and the Redfish URI template uri, whose
// {Name} segments become path parameters. A URI with a trailing slash is
// anchored with {$} so it matches exactly rather than as a subtree.
func Handle(mux *http.ServeMux, method, uri string, h HandlerFunc) {
	pattern := uri
	if len(uri) > 1 && strings.HasSuffix(uri, "/") {
		pattern += "{$}"
	}
	mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
		h(w, r, Params{URI: uri, req: r})
	})
}
