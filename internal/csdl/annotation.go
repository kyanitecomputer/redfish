package csdl

// Annotations is a convenience view over a slice of annotations.
type Annotations []Annotation

// Find returns the first annotation with the given term and whether it exists.
func (as Annotations) Find(term string) (Annotation, bool) {
	for _, a := range as {
		if a.Term == term {
			return a, true
		}
	}
	return Annotation{}, false
}

// String returns the String value of the named annotation term, or "".
func (as Annotations) String(term string) string {
	if a, ok := as.Find(term); ok {
		return a.String
	}
	return ""
}

// Has reports whether the named annotation term is present.
func (as Annotations) Has(term string) bool {
	_, ok := as.Find(term)
	return ok
}

// EnumMember returns the EnumMember value of the named annotation term, or "".
func (as Annotations) EnumMember(term string) string {
	if a, ok := as.Find(term); ok {
		return a.EnumMember
	}
	return ""
}

// UpdatableRecord reports whether the named annotation carries a record with
// Property="Updatable" set to true (Capabilities.UpdateRestrictions).
func (as Annotations) recordBool(term, property string) bool {
	a, ok := as.Find(term)
	if !ok || a.Record == nil {
		return false
	}
	for _, pv := range a.Record.PropertyValues {
		if pv.Property == property && pv.Bool != nil && *pv.Bool {
			return true
		}
	}
	return false
}

// Updatable reports whether Capabilities.UpdateRestrictions marks the type as
// updatable (PATCH-able).
func (as Annotations) Updatable() bool {
	return as.recordBool("Capabilities.UpdateRestrictions", "Updatable")
}

// Strings returns the string members of a Collection-valued annotation, such as
// Redfish.Uris.
func (as Annotations) Strings(term string) []string {
	if a, ok := as.Find(term); ok && a.Collection != nil {
		return a.Collection.Strings
	}
	return nil
}

// Description returns the human-readable OData.Description, if present.
func (as Annotations) Description() string { return as.String("OData.Description") }
