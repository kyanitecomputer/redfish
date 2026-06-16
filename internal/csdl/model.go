// Package csdl parses the DMTF Redfish CSDL schema bundle (OData EDMX, as
// shipped in DSP8010) into an in-memory model and indexes it for the code
// generator.
//
// The parser intentionally models only the subset of OData EDMX that Redfish
// schemas use. It is not a general-purpose EDMX implementation.
package csdl

import "encoding/xml"

// Edmx is the root <edmx:Edmx> element of a single CSDL document.
type Edmx struct {
	XMLName      xml.Name    `xml:"Edmx"`
	Version      string      `xml:"Version,attr"`
	References   []Reference `xml:"Reference"`
	DataServices Services    `xml:"DataServices"`
}

// Reference is an <edmx:Reference> linking to another CSDL document.
type Reference struct {
	URI      string    `xml:"Uri,attr"`
	Includes []Include `xml:"Include"`
}

// Include is an <edmx:Include> declaring a namespace pulled from a Reference.
type Include struct {
	Namespace string `xml:"Namespace,attr"`
	Alias     string `xml:"Alias,attr"`
}

// Services is the <edmx:DataServices> container holding schemas.
type Services struct {
	Schemas []Schema `xml:"Schema"`
}

// Schema is an <edm:Schema>. Redfish files contain one unversioned namespace
// (for example "ServiceRoot") plus many versioned ones ("ServiceRoot.v1_0_0",
// "ServiceRoot.v1_21_0", ...).
type Schema struct {
	Namespace       string            `xml:"Namespace,attr"`
	Alias           string            `xml:"Alias,attr"`
	EntityTypes     []EntityType      `xml:"EntityType"`
	ComplexTypes    []ComplexType     `xml:"ComplexType"`
	EnumTypes       []EnumType        `xml:"EnumType"`
	TypeDefinitions []TypeDefinition  `xml:"TypeDefinition"`
	Containers      []EntityContainer `xml:"EntityContainer"`
	Actions         []Action          `xml:"Action"`
	Annotations     []Annotation      `xml:"Annotation"`
}

// Action is an OData action (<Action>). Redfish actions are bound (IsBound) and
// their first parameter binds the action to a resource's Actions complex type
// (for example Type="ComputerSystem.v1_0_0.Actions").
type Action struct {
	Name        string       `xml:"Name,attr"`
	IsBound     bool         `xml:"IsBound,attr"`
	Parameters  []Parameter  `xml:"Parameter"`
	Annotations []Annotation `xml:"Annotation"`
}

// Parameter is one <Parameter> of an Action. The first parameter of a bound
// action is the binding parameter naming the resource the action applies to.
type Parameter struct {
	Name        string       `xml:"Name,attr"`
	Type        string       `xml:"Type,attr"`
	Nullable    *bool        `xml:"Nullable,attr"`
	Annotations []Annotation `xml:"Annotation"`
}

// EntityType is an addressable Redfish resource type (<EntityType>).
type EntityType struct {
	Name        string               `xml:"Name,attr"`
	BaseType    string               `xml:"BaseType,attr"`
	Abstract    bool                 `xml:"Abstract,attr"`
	Properties  []Property           `xml:"Property"`
	NavProps    []NavigationProperty `xml:"NavigationProperty"`
	Annotations []Annotation         `xml:"Annotation"`
}

// ComplexType is an inline structured type with no independent identity
// (<ComplexType>), such as Status or a resource's Links object.
type ComplexType struct {
	Name        string               `xml:"Name,attr"`
	BaseType    string               `xml:"BaseType,attr"`
	Abstract    bool                 `xml:"Abstract,attr"`
	Properties  []Property           `xml:"Property"`
	NavProps    []NavigationProperty `xml:"NavigationProperty"`
	Annotations []Annotation         `xml:"Annotation"`
}

// EnumType is a string enumeration (<EnumType>).
type EnumType struct {
	Name        string       `xml:"Name,attr"`
	Members     []Member     `xml:"Member"`
	Annotations []Annotation `xml:"Annotation"`
}

// Member is one <Member> of an EnumType.
type Member struct {
	Name        string       `xml:"Name,attr"`
	Annotations []Annotation `xml:"Annotation"`
}

// TypeDefinition aliases an underlying primitive type (<TypeDefinition>), for
// example Resource.Id over Edm.String.
type TypeDefinition struct {
	Name           string       `xml:"Name,attr"`
	UnderlyingType string       `xml:"UnderlyingType,attr"`
	Annotations    []Annotation `xml:"Annotation"`
}

// Property is a structural property of an entity or complex type (<Property>).
type Property struct {
	Name        string       `xml:"Name,attr"`
	Type        string       `xml:"Type,attr"`
	Nullable    *bool        `xml:"Nullable,attr"`
	Annotations []Annotation `xml:"Annotation"`
}

// NavigationProperty is a link to another resource (<NavigationProperty>). In
// Redfish JSON these render as references ({"@odata.id": ...}) rather than
// embedded objects.
type NavigationProperty struct {
	Name           string       `xml:"Name,attr"`
	Type           string       `xml:"Type,attr"`
	Nullable       *bool        `xml:"Nullable,attr"`
	ContainsTarget bool         `xml:"ContainsTarget,attr"`
	Annotations    []Annotation `xml:"Annotation"`
}

// EntityContainer (<EntityContainer>) declares the service entry points. The
// ServiceRoot container's singletons define the top-level resource layout.
type EntityContainer struct {
	Name       string      `xml:"Name,attr"`
	Extends    string      `xml:"Extends,attr"`
	Singletons []Singleton `xml:"Singleton"`
}

// Singleton is one <Singleton> entry point inside an EntityContainer.
type Singleton struct {
	Name string `xml:"Name,attr"`
	Type string `xml:"Type,attr"`
}

// Annotation is an OData/Redfish annotation (<Annotation>). Only the scalar
// forms used by Redfish plus nested Record/Collection are modeled.
type Annotation struct {
	Term       string     `xml:"Term,attr"`
	String     string     `xml:"String,attr"`
	Bool       *bool      `xml:"Bool,attr"`
	EnumMember string     `xml:"EnumMember,attr"`
	Record     *Record    `xml:"Record"`
	Collection *ValueList `xml:"Collection"`
}

// Record is a nested <Record> inside an annotation.
type Record struct {
	PropertyValues []PropertyValue `xml:"PropertyValue"`
	Annotations    []Annotation    `xml:"Annotation"`
}

// PropertyValue is one <PropertyValue> of a Record.
type PropertyValue struct {
	Property   string `xml:"Property,attr"`
	String     string `xml:"String,attr"`
	Bool       *bool  `xml:"Bool,attr"`
	EnumMember string `xml:"EnumMember,attr"`
}

// ValueList is a nested <Collection> inside an annotation, used by Redfish.Uris
// and similar string lists.
type ValueList struct {
	Strings []string `xml:"String"`
}

// Nullable reports the effective nullability. Per OData, properties are
// nullable unless explicitly marked otherwise.
func (p Property) IsNullable() bool { return p.Nullable == nil || *p.Nullable }

// IsNullable reports the effective nullability of a navigation property.
func (n NavigationProperty) IsNullable() bool { return n.Nullable == nil || *n.Nullable }
