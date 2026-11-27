package gen

import (
	"fmt"
	"slices"
	"strings"

	"src.kyanite.computer/redfish/internal/csdl"
	"src.kyanite.computer/redfish/odata"
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
	slices.Sort(order)

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
	entries := [][]byte{odataEntry("Service", "/redfish/v1/")}
	for _, res := range m.Resources {
		if !res.IsCollection || len(res.URIs) == 0 {
			continue
		}
		url := canonicalURI(res.URIs)
		entries = append(entries, odataEntry(lastSegment(url), url))
	}
	b := odata.NewBuilder()
	b.String("@odata.context", "/redfish/v1/$metadata")
	b.String("@odata.id", "/redfish/v1/odata")
	b.RawArray("value", entries)
	return string(b.Bytes())
}

func odataEntry(name, url string) []byte {
	b := odata.NewBuilder()
	b.String("name", name)
	b.String("kind", "Singleton")
	b.String("url", url)
	return b.Bytes()
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
