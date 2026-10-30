package rauc

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"src.kyanite.computer/redfish/task"
	"src.kyanite.computer/redfish/update"
)

// pushBundle uploads a bundle through the update service and waits for its
// task to finish, returning the Task resource.
func pushBundle(t *testing.T, srv *httptest.Server, params string) map[string]any {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	p, _ := mw.CreateFormField("UpdateParameters")
	p.Write([]byte(params))
	file, _ := mw.CreateFormFile("UpdateFile", "bundle.raucb")
	file.Write([]byte("bundle-bytes"))
	mw.Close()

	resp, err := http.Post(srv.URL+update.MultipartPushURI, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	var tk map[string]any
	json.NewDecoder(resp.Body).Decode(&tk)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("push: status %d, body %v", resp.StatusCode, tk)
	}
	taskURI := tk["@odata.id"].(string)

	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := http.Get(srv.URL + taskURI)
		if err != nil {
			t.Fatal(err)
		}
		tk = nil
		json.NewDecoder(r.Body).Decode(&tk)
		r.Body.Close()
		if tk["TaskState"] != "Running" {
			return tk
		}
		if time.Now().After(deadline) {
			t.Fatal("task still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestServicePush(t *testing.T) {
	f, b := newFakeRAUC(t)
	mux := http.NewServeMux()
	tasks := task.New()
	tasks.Register(mux)
	update.New(b, tasks).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f.write(t, "install.out", installOK)
	tk := pushBundle(t, srv, `{"@Redfish.OperationApplyTime":"OnReset"}`)
	if tk["TaskState"] != "Completed" || tk["PercentComplete"] != 100.0 {
		t.Errorf("task = %v", tk)
	}

	f.write(t, "install.out", installFailed)
	f.write(t, "install.rc", "1")
	tk = pushBundle(t, srv, `{}`)
	msgs, _ := tk["Messages"].([]any)
	if tk["TaskState"] != "Exception" || len(msgs) != 2 ||
		!strings.Contains(msgs[1].(map[string]any)["Message"].(string), "signature verification failed") {
		t.Errorf("failed task = %v", tk)
	}

	resp, err := http.Get(srv.URL + update.FirmwareInventoryURI + "/slot_a")
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	json.NewDecoder(resp.Body).Decode(&item)
	resp.Body.Close()
	if item["Version"] != "hps-v3-41-g8dcfb94" {
		t.Errorf("slot_a = %v", item)
	}
}
