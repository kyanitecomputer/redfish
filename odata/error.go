package odata

// Standard Redfish base message identifiers used for protocol-level failures.
// The full message registry (DSP8011) can be layered on later; these cover the
// errors the server itself raises.
const (
	MsgGeneralError     = "Base.1.0.GeneralError"
	MsgResourceNotFound = "Base.1.0.ResourceMissingAtURI"
	MsgMethodNotAllowed = "Base.1.0.ActionNotSupported"
	MsgPreconditionFail = "Base.1.0.PreconditionFailed"
	MsgMalformedJSON    = "Base.1.0.MalformedJSON"
	MsgInternalError    = "Base.1.0.InternalError"
	MsgPropertyNotWrite = "Base.1.0.PropertyNotWritable"
	MsgUnrecognizedProp = "Base.1.0.PropertyUnknown"

	MsgActionParameterMissing      = "Base.1.0.ActionParameterMissing"
	MsgActionParameterNotSupported = "Base.1.0.ActionParameterNotSupported"
	MsgServiceUnavailable          = "Base.1.0.ServiceTemporarilyUnavailable"
)

// ExtendedInfo is one entry of the @Message.ExtendedInfo array.
type ExtendedInfo struct {
	MessageID  string
	Message    string
	Severity   string
	Resolution string
}

// Error is the body of a Redfish error response.
type Error struct {
	Code         string
	Message      string
	ExtendedInfo []ExtendedInfo
}

// Bytes encodes the error as a Redfish error response body:
// {"error": {"code": ..., "message": ..., "@Message.ExtendedInfo": [...]}}.
func (e Error) Bytes() []byte {
	inner := NewBuilder()
	inner.String("code", e.Code)
	inner.String("message", e.Message)
	if len(e.ExtendedInfo) > 0 {
		items := make([][]byte, len(e.ExtendedInfo))
		for i, ei := range e.ExtendedInfo {
			m := NewBuilder()
			m.String("@odata.type", "#Message.v1_1_2.Message")
			m.OptString("MessageId", ei.MessageID)
			m.OptString("Message", ei.Message)
			m.OptString("MessageSeverity", ei.Severity)
			m.OptString("Resolution", ei.Resolution)
			items[i] = m.Bytes()
		}
		inner.RawArray("@Message.ExtendedInfo", items)
	}
	outer := NewBuilder()
	outer.Raw("error", inner.Bytes())
	return outer.Bytes()
}
