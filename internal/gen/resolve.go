package gen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"src.kyanite.computer/redfish/internal/csdl"
)

// Build resolves the named target type references against the bundle and
// produces a Model ready for emission. Targets are unversioned references such
// as "ServiceRoot.ServiceRoot" or "ChassisCollection.ChassisCollection".
func Build(b *csdl.Bundle, pkg string, targets []string) (*Model, error) {
	r := &resolver{
		b:           b,
		model:       &Model{Package: pkg},
		complexSeen: map[string]bool{},
		enumSeen:    map[string]bool{},
	}
	for _, t := range targets {
		if err := r.resource(t); err != nil {
			return nil, fmt.Errorf("gen: target %s: %w", t, err)
		}
	}
	sort.Slice(r.model.Complexes, func(i, j int) bool {
		return r.model.Complexes[i].GoName < r.model.Complexes[j].GoName
	})
	sort.Slice(r.model.Enums, func(i, j int) bool {
		return r.model.Enums[i].GoName < r.model.Enums[j].GoName
	})
	r.model.MetadataXML, r.model.ODataDoc = buildMetadata(b, r.model)
	return r.model, nil
}

type resolver struct {
	b           *csdl.Bundle
	model       *Model
	complexSeen map[string]bool
	enumSeen    map[string]bool
}

var versionSuffix = regexp.MustCompile(`\.v\d+_\d+_\d+$`)

// baseNamespace strips a versioned suffix: "ServiceRoot.v1_21_0" -> "ServiceRoot".
func baseNamespace(ns string) string { return versionSuffix.ReplaceAllString(ns, "") }

// goName builds a unique, exported Go identifier for a nested type from its
// base namespace and local name, e.g. ("ServiceRoot","Links") -> "ServiceRootLinks".
func goName(baseNS, name string) string {
	return ident(strings.ReplaceAll(baseNS, ".", "") + name)
}

// ident sanitizes a string into an exported Go identifier.
func ident(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			// drop separators
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

func (r *resolver) resource(typeRef string) error {
	leafNS, et, ok := r.b.LatestEntity(typeRef)
	if !ok {
		return fmt.Errorf("cannot resolve entity %q", typeRef)
	}
	chain := r.entityChain(et)
	baseNS := baseNamespace(leafNS)

	res := &Resource{
		GoName:      et.Name,
		ODataType:   "#" + leafNS + "." + et.Name,
		ContextFrag: baseNS + "." + et.Name,
		URIs:        chainURIs(r.b, chain),
		Description: chainDescription(chain),
	}

	if isCollectionChain(chain) {
		res.IsCollection = true
		res.MemberGoName, res.ExpandMembers = collectionMember(chain)
		r.model.Resources = append(r.model.Resources, res)
		return nil
	}

	res.Fields = r.flattenEntityFields(chain)
	res.Patchable = chainUpdatable(chain)
	res.Actions = r.boundActions(baseNS)
	r.model.Resources = append(r.model.Resources, res)
	return nil
}

// boundActions converts the CSDL actions bound to the resource base namespace
// into the model's Action form, resolving each action's input parameters. The
// binding (first) parameter is skipped, as are parameters actionParam does not
// support.
func (r *resolver) boundActions(base string) []Action {
	var out []Action
	for _, a := range r.b.BoundActions(base) {
		act := Action{
			GoName:      ident(a.Name),
			WireName:    "#" + base + "." + a.Name,
			Segment:     base + "." + a.Name,
			Description: csdl.Annotations(a.Annotations).Description(),
		}
		for _, p := range a.Parameters[1:] { // [0] is the binding parameter
			if f, ok := r.actionParam(p); ok {
				act.Params = append(act.Params, f)
			}
		}
		out = append(out, act)
	}
	return out
}

// actionParam maps an action parameter to a field. Scalars, enums, string and
// enum arrays, and arrays of resource references are supported; complex
// parameters are skipped.
func (r *resolver) actionParam(p csdl.Parameter) (Field, bool) {
	inner, isColl := csdl.StripCollection(p.Type)
	if _, _, ok := r.b.LatestComplex(inner); ok {
		return Field{}, false
	}
	if isColl {
		if _, _, ok := r.b.ResolveEntity(inner); ok {
			return Field{GoName: ident(p.Name), JSONName: p.Name, GoType: "[]odata.Link", Kind: KindNavSlice}, true
		}
		f, ok := r.scalarField(p.Name, p.Type, true, false)
		return f, ok && f.Kind == KindStringSlice
	}
	f, ok := r.scalarField(p.Name, p.Type, true, false)
	return f, ok && f.scalarKind()
}

// chainUpdatable reports whether any type in the chain is marked updatable.
func chainUpdatable(chain []*csdl.EntityType) bool {
	for _, et := range chain {
		if csdl.Annotations(et.Annotations).Updatable() {
			return true
		}
	}
	return false
}

// entityChain returns the BaseType chain leaf-first.
func (r *resolver) entityChain(leaf *csdl.EntityType) []*csdl.EntityType {
	var chain []*csdl.EntityType
	cur := leaf
	for cur != nil {
		chain = append(chain, cur)
		if cur.BaseType == "" {
			break
		}
		_, base, ok := r.b.ResolveEntity(cur.BaseType)
		if !ok {
			break
		}
		cur = base
	}
	return chain
}

func isCollectionChain(chain []*csdl.EntityType) bool {
	for _, et := range chain {
		if et.Name == "ResourceCollection" {
			return true
		}
	}
	return false
}

// collectionMember returns the member type name and whether the Members nav is
// OData.AutoExpand (members inlined) rather than AutoExpandReferences (links).
func collectionMember(chain []*csdl.EntityType) (name string, autoExpand bool) {
	for _, et := range chain {
		for _, np := range et.NavProps {
			if np.Name == "Members" {
				inner, _ := csdl.StripCollection(np.Type)
				_, name = csdl.SplitQualified(inner)
				autoExpand = csdl.Annotations(np.Annotations).Has("OData.AutoExpand")
				return name, autoExpand
			}
		}
	}
	return "", false
}

func chainURIs(b *csdl.Bundle, chain []*csdl.EntityType) []string {
	for _, et := range chain {
		if u := csdl.Annotations(et.Annotations).Strings("Redfish.Uris"); len(u) > 0 {
			return u
		}
	}
	return nil
}

func chainDescription(chain []*csdl.EntityType) string {
	for _, et := range chain {
		if d := csdl.Annotations(et.Annotations).Description(); d != "" {
			return d
		}
	}
	return ""
}

// flattenEntityFields collects structural and navigation properties across the
// inheritance chain (root-first so derived types override), de-duplicated by
// wire name.
func (r *resolver) flattenEntityFields(chain []*csdl.EntityType) []Field {
	var props []csdl.Property
	var navs []csdl.NavigationProperty
	for i := len(chain) - 1; i >= 0; i-- {
		props = append(props, chain[i].Properties...)
		navs = append(navs, chain[i].NavProps...)
	}
	return r.buildFields(props, navs)
}

func (r *resolver) buildFields(props []csdl.Property, navs []csdl.NavigationProperty) []Field {
	var fields []Field
	seen := map[string]int{}
	add := func(f Field, ok bool) {
		if !ok {
			return
		}
		if idx, dup := seen[f.JSONName]; dup {
			fields[idx] = f
			return
		}
		seen[f.JSONName] = len(fields)
		fields = append(fields, f)
	}
	for _, p := range props {
		anns := csdl.Annotations(p.Annotations)
		f, ok := r.scalarField(p.Name, p.Type, p.IsNullable(), anns.Has("Redfish.Required"))
		f.Writable = strings.HasSuffix(anns.EnumMember("OData.Permissions"), "ReadWrite")
		add(f, ok)
	}
	for _, n := range navs {
		add(r.navField(n))
	}
	return fields
}

func (r *resolver) navField(n csdl.NavigationProperty) (Field, bool) {
	inner, isColl := csdl.StripCollection(n.Type)
	_ = inner
	f := Field{
		GoName:   ident(n.Name),
		JSONName: n.Name,
		Required: csdl.Annotations(n.Annotations).Has("Redfish.Required"),
	}
	if isColl {
		f.Kind = KindNavSlice
		f.GoType = "[]odata.Link"
	} else {
		f.Kind = KindNavSingle
		f.GoType = "odata.Link"
	}
	return f, true
}

// scalarField maps a structural property to a Go field, resolving type
// definitions, enums, and complex types as needed.
func (r *resolver) scalarField(name, typeRef string, nullable, required bool) (Field, bool) {
	inner, isColl := csdl.StripCollection(typeRef)
	ns, tname := csdl.SplitQualified(inner)

	f := Field{GoName: ident(name), JSONName: name, Required: required}

	if ns == "Edm" {
		kind, base := edmKind(tname)
		if isColl {
			if kind == KindString {
				f.Kind, f.GoType = KindStringSlice, "[]string"
				return f, true
			}
			return Field{}, false // non-string scalar arrays unsupported in this subset
		}
		f.Kind = kind
		f.GoType = goScalarType(kind, base, required)
		return f, true
	}

	if td, ok := r.b.ResolveTypeDef(inner); ok {
		// Type definitions alias an Edm primitive; recurse on the underlying type
		// while preserving this property's name and requiredness.
		return r.scalarField(name, td.UnderlyingType, nullable, required)
	}

	if en, ok := r.b.ResolveEnum(inner); ok {
		gn := r.enum(baseNamespace(ns), en)
		if isColl {
			f.Kind, f.GoType = KindStringSlice, "[]string"
			return f, true
		}
		f.Kind, f.GoType = KindEnum, gn
		return f, true
	}

	if cns, ct, ok := r.b.LatestComplex(inner); ok {
		gn := r.complex(cns, ct)
		if isColl {
			f.Kind, f.GoType = KindComplex, "[]"+gn
			// Complex slices are emitted as raw arrays; mark via GoType only.
			return f, true
		}
		f.Kind = KindComplex
		if required {
			f.GoType = gn
		} else {
			f.GoType = "*" + gn
		}
		return f, true
	}

	return Field{}, false // unresolved reference; skipped in this subset
}

func (r *resolver) enum(baseNS string, en *csdl.EnumType) string {
	gn := goName(baseNS, en.Name)
	if r.enumSeen[gn] {
		return gn
	}
	r.enumSeen[gn] = true
	members := make([]string, len(en.Members))
	for i, m := range en.Members {
		members[i] = m.Name
	}
	r.model.Enums = append(r.model.Enums, &Enum{GoName: gn, Members: members})
	return gn
}

func (r *resolver) complex(ns string, ct *csdl.ComplexType) string {
	gn := goName(baseNamespace(ns), ct.Name)
	if r.complexSeen[gn] {
		return gn
	}
	r.complexSeen[gn] = true // mark before recursing to break cycles

	chain := r.complexChain(ct)
	var props []csdl.Property
	var navs []csdl.NavigationProperty
	for i := len(chain) - 1; i >= 0; i-- {
		props = append(props, chain[i].Properties...)
		navs = append(navs, chain[i].NavProps...)
	}
	fields := r.buildFields(props, navs)
	c := &Complex{GoName: gn, Fields: fields}
	// A resource's "Actions" complex carries its bound actions as typed target
	// fields so the body can advertise the actions the service supports.
	if ct.Name == "Actions" {
		c.Actions = r.boundActions(baseNamespace(ns))
	}
	r.model.Complexes = append(r.model.Complexes, c)
	return gn
}

func (r *resolver) complexChain(leaf *csdl.ComplexType) []*csdl.ComplexType {
	var chain []*csdl.ComplexType
	cur := leaf
	for cur != nil {
		chain = append(chain, cur)
		if cur.BaseType == "" {
			break
		}
		_, base, ok := r.b.ResolveComplex(cur.BaseType)
		if !ok {
			break
		}
		cur = base
	}
	return chain
}

func edmKind(t string) (Kind, string) {
	switch t {
	case "Boolean":
		return KindBool, "bool"
	case "Int16", "Int32", "Int64", "Byte", "SByte":
		return KindInt, "int64"
	case "Decimal", "Double", "Single":
		return KindFloat, "float64"
	default: // String, Guid, DateTimeOffset, Duration, Date, TimeOfDay, PrimitiveType
		return KindString, "string"
	}
}

func goScalarType(kind Kind, base string, required bool) string {
	if required || kind == KindString {
		return base
	}
	return "*" + base
}
