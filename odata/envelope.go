package odata

// Wire constants for the Redfish HTTP surface.
const (
	// ContentType is the media type for all Redfish JSON payloads.
	ContentType = "application/json; charset=utf-8"
	// Version is the implemented Redfish/OData protocol version, sent in the
	// OData-Version response header.
	Version = "4.0"
	// MetadataPath is the service metadata document location, used when
	// building @odata.context values.
	MetadataPath = "/redfish/v1/$metadata"
)

// Context builds an @odata.context value for a resource fragment, for example
// Context("ServiceRoot.ServiceRoot") -> "/redfish/v1/$metadata#ServiceRoot.ServiceRoot".
func Context(fragment string) string {
	return MetadataPath + "#" + fragment
}
