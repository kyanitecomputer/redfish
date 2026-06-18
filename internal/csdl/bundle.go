package csdl

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Bundle is a parsed, indexed collection of CSDL documents. Schemas are indexed
// by their full namespace (versioned and unversioned alike).
type Bundle struct {
	docs    []*Edmx
	schemas map[string]*Schema
}

// LoadDir parses every *.xml file in dir into a Bundle.
func LoadDir(dir string) (*Bundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("csdl: read schema dir: %w", err)
	}
	b := &Bundle{schemas: make(map[string]*Schema)}
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

// LoadFS parses every *.xml file in fsys into a Bundle.
func LoadFS(fsys fs.FS) (*Bundle, error) {
	b := &Bundle{schemas: make(map[string]*Schema)}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("csdl: read %s: %w", path, err)
		}
		return b.add(data, path)
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bundle) add(data []byte, name string) error {
	doc, err := Parse(data)
	if err != nil {
		return fmt.Errorf("csdl: parse %s: %w", name, err)
	}
	b.docs = append(b.docs, doc)
	for i := range doc.DataServices.Schemas {
		s := &doc.DataServices.Schemas[i]
		b.schemas[s.Namespace] = s
	}
	return nil
}

// Schema returns the schema for an exact namespace.
func (b *Bundle) Schema(namespace string) (*Schema, bool) {
	s, ok := b.schemas[namespace]
	return s, ok
}

var versionRE = regexp.MustCompile(`^(.+)\.v(\d+)_(\d+)_(\d+)$`)

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
	var (
		bestNS string
		best   version
		found  bool
	)
	for ns := range b.schemas {
		m := versionRE.FindStringSubmatch(ns)
		if m == nil || m[1] != base {
			continue
		}
		v := version{atoi(m[2]), atoi(m[3]), atoi(m[4])}
		if !found || best.less(v) {
			best, bestNS, found = v, ns, true
		}
	}
	return bestNS, found
}

// LatestContainer returns the highest-versioned namespace under base that
// declares an EntityContainer with the given name, for example
// LatestContainer("ServiceRoot", "ServiceContainer").
func (b *Bundle) LatestContainer(base, container string) (string, bool) {
	var (
		bestNS string
		best   version
		found  bool
	)
	for ns, s := range b.schemas {
		m := versionRE.FindStringSubmatch(ns)
		if m == nil || m[1] != base {
			continue
		}
		for i := range s.Containers {
			if s.Containers[i].Name != container {
				continue
			}
			v := version{atoi(m[2]), atoi(m[3]), atoi(m[4])}
			if !found || best.less(v) {
				best, bestNS, found = v, ns, true
			}
		}
	}
	return bestNS, found
}

// baseOf returns the unversioned base of a namespace: "ComputerSystem.v1_0_0"
// -> "ComputerSystem". An already-unversioned namespace is returned unchanged.
func baseOf(ns string) string {
	if m := versionRE.FindStringSubmatch(ns); m != nil {
		return m[1]
	}
	return ns
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
	for ns, s := range b.schemas {
		if baseOf(ns) != base {
			continue
		}
		var v version
		if m := versionRE.FindStringSubmatch(ns); m != nil {
			v = version{atoi(m[2]), atoi(m[3]), atoi(m[4])}
		}
		for _, a := range s.Actions {
			if !a.IsBound || len(a.Parameters) == 0 {
				continue
			}
			bindNS, bindName := SplitQualified(stripColl(a.Parameters[0].Type))
			if bindName != "Actions" || baseOf(bindNS) != base {
				continue
			}
			if cur, ok := best[a.Name]; !ok || cur.ver.less(v) {
				best[a.Name] = found{act: a, ver: v}
			}
		}
	}
	out := make([]Action, 0, len(best))
	for _, f := range best {
		out = append(out, f.act)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
func isVersioned(ns string) bool { return versionRE.MatchString(ns) }

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
func (b *Bundle) LatestComplex(typeRef string) (namespace string, ct *ComplexType, ok bool) {
	inner, _ := StripCollection(typeRef)
	nsRef, name := SplitQualified(inner)
	if !isVersioned(nsRef) {
		var (
			bestNS string
			best   version
			found  bool
		)
		for ns, s := range b.schemas {
			m := versionRE.FindStringSubmatch(ns)
			if m == nil || m[1] != nsRef {
				continue
			}
			for i := range s.ComplexTypes {
				if s.ComplexTypes[i].Name != name {
					continue
				}
				v := version{atoi(m[2]), atoi(m[3]), atoi(m[4])}
				if !found || best.less(v) {
					best, bestNS, found = v, ns, true
				}
			}
		}
		if found {
			ct, _ := b.complexIn(bestNS, name)
			return bestNS, ct, true
		}
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
