package odata

// Link is a Redfish navigation target, holding the @odata.id of the referenced
// resource. The empty Link denotes an absent reference. Generated marshalers
// render a non-empty Link as {"@odata.id": string(link)}.
type Link string

// Links converts a slice of @odata.id strings into a slice of Links.
func Links(ids []string) []Link {
	out := make([]Link, len(ids))
	for i, id := range ids {
		out[i] = Link(id)
	}
	return out
}

// Strings converts a slice of Links back into plain @odata.id strings, the form
// the Builder reference helpers accept.
func Strings(links []Link) []string {
	out := make([]string, len(links))
	for i, l := range links {
		out[i] = string(l)
	}
	return out
}
