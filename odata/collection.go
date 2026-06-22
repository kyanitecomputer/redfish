package odata

// Collection renders a Redfish resource collection. Members are emitted as
// reference links by default; Expanded, when set, is emitted instead as inline
// member objects (for $expand support).
type Collection struct {
	ODataType    string
	ODataID      string
	ODataContext string
	Name         string
	Description  string
	Members      []string // member @odata.id values
	Expanded     [][]byte // pre-encoded inline members; takes precedence
}

// Bytes encodes the collection as Redfish JSON.
func (c Collection) Bytes() []byte {
	b := NewBuilder()
	b.OptString("@odata.context", c.ODataContext)
	b.String("@odata.type", c.ODataType)
	b.String("@odata.id", c.ODataID)
	b.String("Name", c.Name)
	b.OptString("Description", c.Description)
	count := len(c.Members)
	if c.Expanded != nil {
		count = len(c.Expanded)
		b.RawArray("Members", c.Expanded)
	} else {
		b.RefArray("Members", c.Members)
	}
	b.Int("Members@odata.count", int64(count))
	return b.Bytes()
}
