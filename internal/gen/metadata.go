package gen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"src.kyanite.computer/redfish/internal/csdl"
)

const dmtfSchemaBase = "http://redfish.dmtf.org/schemas/v1/"

// buildMetadata produces the service $metadata (EDMX) document and the
// /redfish/v1/odata service document for the resources in the model. The EDMX
// references the canonical DMTF schema URIs; a validator resolves them against
// its local schema cache by file name.
func buildMetadata(b *csdl.Bundle, m *Model) (metadataXML, odataDoc string) {
	return buildEDMX(b, m), buildODataDoc(m)
}

type schemaRef struct {
	file     string
	includes []string
}

func buildEDMX(b *csdl.Bundle, m *Model) string {
	refs := map[string]*schemaRef{}
	var order []string
	addInclude := func(r *schemaRef, ns string) {
		if slices.Contains(r.includes, ns) {
			return
		}
		r.includes = append(r.includes, ns)
	}
	add := func(baseNS, verNS string) {
		r, ok := refs[baseNS]
		if !ok {
			r = &schemaRef{file: baseNS + "_v1.xml"}
			refs[baseNS] = r
			order = append(order, baseNS)
		}
		addInclude(r, baseNS)
		if verNS != baseNS {
			addInclude(r, verNS)
		}
	}

	for _, res := range m.Resources {
		verNS := namespaceOfODataType(res.ODataType)
		add(baseNamespace(verNS), verNS)
	}
	sort.Strings(order)

	var b2 strings.Builder
	b2.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b2.WriteString(`<edmx:Edmx xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx" Version="4.0">` + "\n")
	for _, baseNS := range order {
		r := refs[baseNS]
		fmt.Fprintf(&b2, "  <edmx:Reference Uri=%q>\n", dmtfSchemaBase+r.file)
		for _, inc := range r.includes {
			fmt.Fprintf(&b2, "    <edmx:Include Namespace=%q/>\n", inc)
		}
		b2.WriteString("  </edmx:Reference>\n")
	}
	b2.WriteString("  <edmx:DataServices>\n")
	b2.WriteString(`    <Schema xmlns="http://docs.oasis-open.org/odata/ns/edm" Namespace="Service">` + "\n")
	if cns, ok := b.LatestContainer("ServiceRoot", "ServiceContainer"); ok {
		fmt.Fprintf(&b2, "      <EntityContainer Name=\"Service\" Extends=%q/>\n", cns+".ServiceContainer")
	}
	b2.WriteString("    </Schema>\n")
	b2.WriteString("  </edmx:DataServices>\n")
	b2.WriteString("</edmx:Edmx>\n")
	return b2.String()
}

func buildODataDoc(m *Model) string {
	b := newJSONDoc()
	b.field("@odata.context", "/redfish/v1/$metadata")
	b.field("@odata.id", "/redfish/v1/odata")
	var entries []string
	entries = append(entries, odataEntry("Service", "/redfish/v1/"))
	for _, res := range m.Resources {
		if !res.IsCollection || len(res.URIs) == 0 {
			continue
		}
		url := canonicalURI(res.URIs)
		entries = append(entries, odataEntry(lastSegment(url), url))
	}
	b.rawField("value", "["+strings.Join(entries, ",")+"]")
	return b.done()
}

func odataEntry(name, url string) string {
	d := newJSONDoc()
	d.field("name", name)
	d.field("kind", "Singleton")
	d.field("url", url)
	return d.done()
}

// namespaceOfODataType extracts the namespace from an @odata.type value:
// "#ServiceRoot.v1_21_0.ServiceRoot" -> "ServiceRoot.v1_21_0".
func namespaceOfODataType(t string) string {
	t = strings.TrimPrefix(t, "#")
	ns, _ := csdl.SplitQualified(t)
	return ns
}

func lastSegment(uri string) string {
	uri = strings.TrimRight(uri, "/")
	if i := strings.LastIndexByte(uri, '/'); i >= 0 {
		return uri[i+1:]
	}
	return uri
}

// jsonDoc is a tiny ordered JSON object writer used only for the static
// metadata documents emitted at generation time.
type jsonDoc struct {
	b     strings.Builder
	dirty bool
}

func newJSONDoc() *jsonDoc {
	d := &jsonDoc{}
	d.b.WriteByte('{')
	return d
}

func (d *jsonDoc) key(name string) {
	if d.dirty {
		d.b.WriteByte(',')
	}
	d.dirty = true
	fmt.Fprintf(&d.b, "%q:", name)
}

func (d *jsonDoc) field(name, value string) {
	d.key(name)
	fmt.Fprintf(&d.b, "%q", value)
}

func (d *jsonDoc) rawField(name, raw string) {
	d.key(name)
	d.b.WriteString(raw)
}

func (d *jsonDoc) done() string {
	d.b.WriteByte('}')
	return d.b.String()
}
