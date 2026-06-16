package csdl

import (
	"encoding/xml"
	"fmt"
)

// Parse decodes a single CSDL document from raw XML bytes.
func Parse(data []byte) (*Edmx, error) {
	var doc Edmx
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("csdl: decode document: %w", err)
	}
	return &doc, nil
}
