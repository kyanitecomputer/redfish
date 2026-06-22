package odata

// ActionTarget is the value of a Redfish action member: the POST target URI and
// an optional @Redfish.ActionInfo reference. A resource advertises an action by
// setting the corresponding field on its generated Actions struct.
type ActionTarget struct {
	Target     string // POST URI, e.g. "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"
	ActionInfo string // optional ActionInfo resource @odata.id
}

// Bytes encodes the action target object: {"target": ..., "@Redfish.ActionInfo": ...}.
func (a *ActionTarget) Bytes() []byte {
	b := NewBuilder()
	b.String("target", a.Target)
	b.OptString("@Redfish.ActionInfo", a.ActionInfo)
	return b.Bytes()
}
