package update

import (
	"context"
	"errors"
	"io"
	"net/http"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// MultipartPushURI is the MultipartHttpPushUri the service accepts update
// images on.
const MultipartPushURI = ServiceURI + "/upload"

// maxParamsBytes bounds the size of the UpdateParameters part.
const maxParamsBytes = 64 << 10

// errInstallReturned unblocks an upload whose Install returned before reading
// the whole image.
var errInstallReturned = errors.New("update: install returned before reading the image")

// servePush handles a multipart HTTP push update (DSP0266): a
// multipart/form-data body with an optional UpdateParameters JSON part followed
// by the UpdateFile part. The image is streamed to the backend's Install while
// the request is read, and the response is 202 Accepted with the task monitor.
func (s *Service) servePush(w http.ResponseWriter, r *http.Request, _ server.Params) {
	if s.MaxImageSizeBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, s.MaxImageSizeBytes)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		server.BadRequest(w, odata.MsgUnrecognizedBody, "The request body must be multipart/form-data.")
		return
	}

	var req Request
	var file io.Reader
	for file == nil {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeUploadErr(w, err)
			return
		}
		switch part.FormName() {
		case "UpdateParameters":
			body, err := io.ReadAll(io.LimitReader(part, maxParamsBytes))
			if err != nil {
				writeUploadErr(w, err)
				return
			}
			// UpdateParameters carries a subset of the SimpleUpdate
			// parameters (Targets, ExcludeTargets, ForceUpdate, Stage), so the
			// generated SimpleUpdate parser reads it.
			var p gen.UpdateServiceSimpleUpdateParameters
			if err := p.UnmarshalJSON(body); err != nil {
				server.BadRequest(w, odata.MsgMalformedJSON, "The UpdateParameters part was not valid JSON.")
				return
			}
			req = Request{
				Targets:        p.Targets,
				ExcludeTargets: p.ExcludeTargets,
				ForceUpdate:    p.ForceUpdate != nil && *p.ForceUpdate,
				Stage:          p.Stage != nil && *p.Stage,
			}
		case "UpdateFile":
			file = part
		}
	}
	if file == nil {
		server.BadRequest(w, odata.MsgActionParameterMissing, "The request requires an UpdateFile part.")
		return
	}

	pr, pw := io.Pipe()
	inst := s.backend.(Installer)
	res, err := s.start("Multipart Update", func(ctx context.Context, progress func(int)) error {
		err := inst.Install(ctx, Image{Body: pr}, req, progress)
		pr.CloseWithError(errInstallReturned)
		return err
	})
	if err != nil {
		server.WriteErr(w, err)
		return
	}
	if _, err := io.Copy(pw, file); err != nil && !errors.Is(err, errInstallReturned) {
		pw.CloseWithError(err)
		writeUploadErr(w, err)
		return
	}
	pw.Close()
	server.WriteActionResult(w, res)
}

// writeUploadErr reports a failure to read the upload: 413 when it exceeds
// MaxImageSizeBytes, otherwise 400.
func writeUploadErr(w http.ResponseWriter, err error) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		server.WriteError(w, http.StatusRequestEntityTooLarge, odata.MsgGeneralError,
			"The update image exceeds the maximum size the service accepts.")
		return
	}
	server.BadRequest(w, odata.MsgUnrecognizedBody, "The multipart request body could not be read.")
}
