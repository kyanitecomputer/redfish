package csdl

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Bundle is a parsed, indexed collection of CSDL documents. Schemas are indexed
// by their full namespace (versioned and unversioned alike), and by their
// unversioned base namespace.
type Bundle struct {
	schemas map[string]*Schema
	byBase  map[string][]versioned
}

// versioned is a schema of a base namespace: one of its versions, or the
// unversioned namespace itself.
type versioned struct {
	ns     string
	v      version
	hasVer bool // ns has a vX_Y_Z suffix
	s      *Schema
}

func newBundle() *Bundle {
	return &Bundle{schemas: map[string]*Schema{}, byBase: map[string][]versioned{}}
}

// LoadDir parses every *.xml file in dir into a Bundle.
func LoadDir(dir string) (*Bundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("csdl: read schema dir: %w", err)
	}
	b := newBundle()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("csdl: read %s: %w", e.Name(), err)
		}
		if err := b.add(data, e.Name()); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *Bundle) add(data []byte, name string) error {
	doc, err := Parse(data)
	if err != nil {
		return fmt.Errorf("csdl: parse %s: %w", name, err)
	}
	for i := range doc.DataServices.Schemas {
		s := &doc.DataServices.Schemas[i]
		if _, dup := b.schemas[s.Namespace]; dup {
			return fmt.Errorf("csdl: %s defines namespace %s again", name, s.Namespace)
		}
		b.schemas[s.Namespace] = s
		base, v, ok := splitVersion(s.Namespace)
		b.byBase[base] = append(b.byBase[base], versioned{ns: s.Namespace, v: v, hasVer: ok, s: s})
	}
	return nil
}

// Schema returns the schema for an exact namespace.
func (b *Bundle) Schema(namespace string) (*Schema, bool) {
	s, ok := b.schemas[namespace]
	return s, ok
}

// version represents a parsed vMAJOR_MINOR_ERRATA namespace suffix.
type version struct{ major, minor, errata int }

func (v version) less(o version) bool {
	switch {
	case v.major != o.major:
		return v.major < o.major
	case v.minor != o.minor:
		return v.minor < o.minor
	default:
		return v.errata < o.errata
	}
}

// LatestNamespace returns the highest versioned namespace for a base name (for
// example base "ServiceRoot" -> "ServiceRoot.v1_21_0"). It reports false when
// no versioned namespace exists for the base.
func (b *Bundle) LatestNamespace(base string) (string, bool) {
	best, ok := b.latest(base, func(*Schema) bool { return true })
	return best.ns, ok
}

// latest returns the highest version of base whose schema has, by has, the
// definition looked for.
func (b *Bundle) latest(base string, has func(*Schema) bool) (versioned, bool) {
	var best versioned
	found := false
	for _, vs := range b.byBase[base] {
		if vs.hasVer && has(vs.s) && (!found || best.v.less(vs.v)) {
			best, found = vs, true
		}
	}
	return best, found
}

// LatestContainer returns the highest-versioned namespace under base that
// declares an EntityContainer with the given name, for example
// LatestContainer("ServiceRoot", "ServiceContainer").
func (b *Bundle) LatestContainer(base, container string) (string, bool) {
	best, ok := b.latest(base, func(s *Schema) bool {
		return slices.ContainsFunc(s.Containers, func(c EntityContainer) bool { return c.Name == container })
	})
	return best.ns, ok
}

var versionRE = regexp.MustCompile(`^(.+)\.v(\d+)_(\d+)_(\d+)$`)

// splitVersion splits a namespace into its unversioned base and its version:
// "ComputerSystem.v1_0_0" -> ("ComputerSystem", 1.0.0, true). An unversioned
// namespace is its own base.
func splitVersion(ns string) (base string, v version, ok bool) {
	m := versionRE.FindStringSubmatch(ns)
	if m == nil {
		return ns, version{}, false
	}
	return m[1], version{atoi(m[2]), atoi(m[3]), atoi(m[4])}, true
}

// BaseNamespace returns the unversioned base of a namespace:
// "ComputerSystem.v1_0_0" -> "ComputerSystem". An unversioned namespace is
// returned unchanged.
func BaseNamespace(ns string) string {
	base, _, _ := splitVersion(ns)
	return base
}

// Defines reports whether the unversioned namespace base, or one of its
// versions, defines an entity, complex, enum or type definition named name.
func (b *Bundle) Defines(base, name string) bool {
	for _, vs := range b.byBase[base] {
		s := vs.s
		if slices.ContainsFunc(s.EntityTypes, func(t EntityType) bool { return t.Name == name }) ||
			slices.ContainsFunc(s.ComplexTypes, func(t ComplexType) bool { return t.Name == name }) ||
			slices.ContainsFunc(s.EnumTypes, func(t EnumType) bool { return t.Name == name }) ||
			slices.ContainsFunc(s.TypeDefinitions, func(t TypeDefinition) bool { return t.Name == name }) {
			return true
		}
	}
	return false
}

// BoundActions returns the actions bound to the resource identified by its
// unversioned base namespace (for example "ComputerSystem"). An action is bound
// to a resource when its binding (first) parameter is the resource's Actions
// complex type. Actions defined across several versioned namespaces are
// de-duplicated by name, keeping the highest-versioned definition so the latest
// parameter set wins. The result is sorted by action name for determinism.
func (b *Bundle) BoundActions(base string) []Action {
	type found struct {
		act Action
		ver version
	}
	best := map[string]found{}
	for _, vs := range b.byBase[base] {
		for _, a := range vs.s.Actions {
			if !a.IsBound || len(a.Parameters) == 0 {
				continue
			}
			bindNS, bindName := SplitQualified(stripColl(a.Parameters[0].Type))
			if bindName != "Actions" || BaseNamespace(bindNS) != base {
				continue
			}
			if cur, ok := best[a.Name]; !ok || cur.ver.less(vs.v) {
				best[a.Name] = found{act: a, ver: vs.v}
			}
		}
	}
	out := make([]Action, 0, len(best))
	for _, f := range best {
		out = append(out, f.act)
	}
	slices.SortFunc(out, func(a, b Action) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// stripColl is StripCollection's inner-type result, ignoring the boolean.
func stripColl(typeRef string) string {
	inner, _ := StripCollection(typeRef)
	return inner
}

// StripCollection unwraps a Collection(T) type reference, returning the inner
// type and whether the reference was a collection.
func StripCollection(typeRef string) (inner string, isCollection bool) {
	if strings.HasPrefix(typeRef, "Collection(") && strings.HasSuffix(typeRef, ")") {
		return typeRef[len("Collection(") : len(typeRef)-1], true
	}
	return typeRef, false
}

// SplitQualified splits a qualified type name into its namespace and local
// name on the final dot: "ServiceRoot.v1_0_0.Links" -> ("ServiceRoot.v1_0_0",
// "Links").
func SplitQualified(qualified string) (namespace, name string) {
	i := strings.LastIndex(qualified, ".")
	if i < 0 {
		return "", qualified
	}
	return qualified[:i], qualified[i+1:]
}

// isVersioned reports whether a namespace already carries a vX_Y_Z suffix.
func isVersioned(ns string) bool {
	_, _, ok := splitVersion(ns)
	return ok
}

// candidateNamespaces returns the namespaces to search for an exact reference:
// the namespace as written first, then its latest version as a fallback for
// unversioned references whose only definitions are versioned. The exact
// namespace is always tried first so that BaseType chains (which reference
// abstract, unversioned base types by their exact namespace) never jump to a
// versioned leaf and loop.
func (b *Bundle) candidateNamespaces(ns string) []string {
	if isVersioned(ns) {
		return []string{ns}
	}
	if _, ok := b.schemas[ns]; ok {
		return []string{ns}
	}
	if latest, ok := b.LatestNamespace(ns); ok {
		return []string{latest}
	}
	return []string{ns}
}

// ResolveEntity resolves an entity type reference to the definition in the
// exact namespace named (Collection(...) wrappers are unwrapped). Unversioned
// references resolve to their latest version only when no schema exists for the
// unversioned namespace itself. Use LatestEntity to obtain the concrete,
// servable leaf type for an abstract resource.
func (b *Bundle) ResolveEntity(typeRef string) (namespace string, et *EntityType, ok bool) {
	inner, _ := StripCollection(typeRef)
	ns, name := SplitQualified(inner)
	for _, candidate := range b.candidateNamespaces(ns) {
		if et, ok := b.entityIn(candidate, name); ok {
			return candidate, et, true
		}
	}
	return "", nil, false
}

// LatestEntity resolves a reference to the latest versioned concrete entity
// type. For an abstract resource such as "ServiceRoot.ServiceRoot" it returns
// the highest-version leaf ("ServiceRoot.v1_21_0.ServiceRoot") that a service
// actually serves.
func (b *Bundle) LatestEntity(typeRef string) (namespace string, et *EntityType, ok bool) {
	inner, _ := StripCollection(typeRef)
	ns, name := SplitQualified(inner)
	if !isVersioned(ns) {
		if latest, ok := b.LatestNamespace(ns); ok {
			if et, ok := b.entityIn(latest, name); ok {
				return latest, et, true
			}
		}
	}
	return b.ResolveEntity(typeRef)
}

// LatestComplex resolves a complex type reference to the highest-versioned
// namespace that actually defines it. Some shared complex types (for example
// IPAddresses.IPv4Address) declare an abstract, empty type in their unversioned
// namespace and the concrete, property-bearing type only in a versioned one;
// exact resolution of the unversioned reference would yield the empty abstract.
//
// Versioned references (for example Manager.v1_0_0.Links) are upgraded the same
// way: later schema versions redefine the type deriving from the earlier one,
// so the latest definition carries every property added since.
func (b *Bundle) LatestComplex(typeRef string) (namespace string, ct *ComplexType, ok bool) {
	inner, _ := StripCollection(typeRef)
	nsRef, name := SplitQualified(inner)
	best, ok := b.latest(BaseNamespace(nsRef), func(s *Schema) bool {
		return slices.ContainsFunc(s.ComplexTypes, func(c ComplexType) bool { return c.Name == name })
	})
	if ok {
		ct, _ := b.complexIn(best.ns, name)
		return best.ns, ct, true
	}
	return b.ResolveComplex(typeRef)
}

// ResolveComplex resolves a complex type reference to its definition in the
// exact namespace named.
func (b *Bundle) ResolveComplex(typeRef string) (namespace string, ct *ComplexType, ok bool) {
	inner, _ := StripCollection(typeRef)
	ns, name := SplitQualified(inner)
	for _, candidate := range b.candidateNamespaces(ns) {
		if ct, ok := b.complexIn(candidate, name); ok {
			return candidate, ct, true
		}
	}
	return "", nil, false
}

// ResolveEnum resolves an enum type reference to its concrete definition.
func (b *Bundle) ResolveEnum(typeRef string) (*EnumType, bool) {
	inner, _ := StripCollection(typeRef)
	ns, name := SplitQualified(inner)
	for _, candidate := range b.candidateNamespaces(ns) {
		if s, ok := b.schemas[candidate]; ok {
			for i := range s.EnumTypes {
				if s.EnumTypes[i].Name == name {
					return &s.EnumTypes[i], true
				}
			}
		}
	}
	return nil, false
}

// ResolveTypeDef resolves a type definition (primitive alias) reference.
func (b *Bundle) ResolveTypeDef(typeRef string) (*TypeDefinition, bool) {
	inner, _ := StripCollection(typeRef)
	ns, name := SplitQualified(inner)
	for _, candidate := range b.candidateNamespaces(ns) {
		if s, ok := b.schemas[candidate]; ok {
			for i := range s.TypeDefinitions {
				if s.TypeDefinitions[i].Name == name {
					return &s.TypeDefinitions[i], true
				}
			}
		}
	}
	return nil, false
}

func (b *Bundle) entityIn(ns, name string) (*EntityType, bool) {
	s, ok := b.schemas[ns]
	if !ok {
		return nil, false
	}
	for i := range s.EntityTypes {
		if s.EntityTypes[i].Name == name {
			return &s.EntityTypes[i], true
		}
	}
	return nil, false
}

func (b *Bundle) complexIn(ns, name string) (*ComplexType, bool) {
	s, ok := b.schemas[ns]
	if !ok {
		return nil, false
	}
	for i := range s.ComplexTypes {
		if s.ComplexTypes[i].Name == name {
			return &s.ComplexTypes[i], true
		}
	}
	return nil, false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
