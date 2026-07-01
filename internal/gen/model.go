// Package gen turns the parsed CSDL bundle into reflection-free Go source: pure
// DTOs, explicit odata.Builder-based marshalers, per-resource Register
// functions, and provider interfaces. Only resources named in the allowlist are
// generated, so unimplemented schemas never reach a consuming binary.
package gen

// Kind classifies how a field is mapped to Go and emitted as JSON.
type Kind int

const (
	KindString      Kind = iota // Go string
	KindBool                    // Go bool / *bool
	KindInt                     // Go int64 / *int64
	KindFloat                   // Go float64 / *float64
	KindStringSlice             // Go []string
	KindEnum                    // Go string-backed enum type
	KindComplex                 // Go nested struct type
	KindNavSingle               // Go odata.Link (single reference)
	KindNavSlice                // Go []odata.Link (reference array)
)

// Field is one property of a resource or complex type.
type Field struct {
	GoName   string // exported Go field name
	JSONName string // Redfish wire property name
	GoType   string // Go type as written in the struct definition
	Kind     Kind
	Required bool // Redfish.Required or a non-nullable key property
	Writable bool // OData.Permissions grants ReadWrite (PATCH-able)
}

// scalarKind reports whether the field is a scalar that the PATCH path supports.
func (f Field) scalarKind() bool {
	switch f.Kind {
	case KindString, KindBool, KindInt, KindFloat, KindEnum:
		return true
	default:
		return false
	}
}

// Resource is a top-level, addressable Redfish resource to generate.
type Resource struct {
	GoName        string   // Go type / identifier base, e.g. "ServiceRoot"
	ODataType     string   // "#ServiceRoot.v1_21_0.ServiceRoot"
	ContextFrag   string   // "$metadata" fragment, e.g. "ServiceRoot.ServiceRoot"
	URIs          []string // serving URIs from Redfish.Uris
	Description   string
	IsCollection  bool
	MemberGoName  string   // collection member resource name (for naming/provider)
	ExpandMembers bool     // Members nav is OData.AutoExpand (inline full members)
	Fields        []Field  // singleton fields (empty for collections)
	Patchable     bool     // Capabilities.UpdateRestrictions marks it updatable
	Actions       []Action // bound actions (POST .../Actions/<Type>.<Action>)
}

// Action is a bound Redfish action exposed under a resource's Actions object and
// invoked by POSTing to its target URI.
type Action struct {
	GoName      string  // action name, e.g. "Reset"
	WireName    string  // Actions member key, e.g. "#ComputerSystem.Reset"
	Segment     string  // URI segment, e.g. "ComputerSystem.Reset"
	Description string  // OData.Description
	Params      []Field // parseable (scalar/enum) input parameters
}

// WritableFields returns the patch-supported writable scalar fields.
func (r *Resource) WritableFields() []Field {
	var out []Field
	for _, f := range r.Fields {
		if f.Writable && f.scalarKind() {
			out = append(out, f)
		}
	}
	return out
}

// Complex is a generated nested struct type.
type Complex struct {
	GoName  string
	Fields  []Field
	Actions []Action // populated for resource "Actions" complex types
}

// Enum is a generated string-backed enumeration.
type Enum struct {
	GoName  string
	Members []string
}

// Model is the complete set of types to emit into one Go package.
type Model struct {
	Package     string
	Resources   []*Resource
	Complexes   []*Complex
	Enums       []*Enum
	MetadataXML string // generated service $metadata document (EDMX)
	ODataDoc    string // generated /redfish/v1/odata service document (JSON)
}
