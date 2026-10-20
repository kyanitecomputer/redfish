package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

func newTestServer(t *testing.T) (*Manager, *httptest.Server) {
	t.Helper()
	m := New()
	mux := http.NewServeMux()
	m.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return m, srv
}

func get(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

// waitDone polls a task monitor until it stops answering 202.
func waitDone(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, m := get(t, url)
		if resp.StatusCode != http.StatusAccepted {
			return resp, m
		}
		if time.Now().After(deadline) {
			t.Fatalf("task monitor %s still running", url)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTaskLifecycle(t *testing.T) {
	m, srv := newTestServer(t)
	release := make(chan struct{})
	reported := make(chan struct{})
	res := m.Start("Update", func(_ context.Context, progress func(int)) error {
		progress(40)
		close(reported)
		<-release
		return nil
	})
	if res.TaskMonitor != MonitorURI+"/1" {
		t.Fatalf("TaskMonitor = %q", res.TaskMonitor)
	}
	<-reported

	resp, body := get(t, srv.URL+res.TaskMonitor)
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Location") != res.TaskMonitor {
		t.Fatalf("running monitor: status %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if body["TaskState"] != "Running" || body["PercentComplete"] != 40.0 {
		t.Errorf("running task = %v", body)
	}

	close(release)
	resp, _ = waitDone(t, srv.URL+res.TaskMonitor)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("finished monitor: status %d, want 204", resp.StatusCode)
	}
	_, body = get(t, srv.URL+CollectionURI+"/1")
	if body["TaskState"] != "Completed" || body["TaskStatus"] != "OK" || body["PercentComplete"] != 100.0 {
		t.Errorf("completed task = %v", body)
	}
	if body["@odata.id"] != CollectionURI+"/1" || body["TaskMonitor"] != res.TaskMonitor {
		t.Errorf("task links = %v", body)
	}
}

func TestTaskFailure(t *testing.T) {
	m, srv := newTestServer(t)
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantID     string
	}{
		{"plain", errors.New("flash write failed"), http.StatusInternalServerError, odata.MsgGeneralError},
		{"typed", &server.Error{Status: http.StatusBadRequest, MessageID: odata.MsgMalformedJSON, Message: "bad image"},
			http.StatusBadRequest, odata.MsgMalformedJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := m.Start("Update", func(context.Context, func(int)) error { return tt.err })
			resp, _ := waitDone(t, srv.URL+res.TaskMonitor)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("monitor status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			id := res.TaskMonitor[len(MonitorURI)+1:]
			_, body := get(t, srv.URL+CollectionURI+"/"+id)
			if body["TaskState"] != "Exception" || body["TaskStatus"] != "Critical" {
				t.Errorf("failed task = %v", body)
			}
			msgs, _ := body["Messages"].([]any)
			if len(msgs) != 2 || msgs[1].(map[string]any)["MessageId"] != tt.wantID {
				t.Errorf("Messages = %v, want second MessageId %s", msgs, tt.wantID)
			}
		})
	}
}

func TestTaskRetention(t *testing.T) {
	m, srv := newTestServer(t)
	var last string
	for range retainCompleted + 5 {
		last = m.Start("Noop", func(context.Context, func(int)) error { return nil }).TaskMonitor
		waitDone(t, srv.URL+last)
	}
	m.Start("Noop", func(context.Context, func(int)) error { return nil })

	_, body := get(t, srv.URL+CollectionURI)
	count := int(body["Members@odata.count"].(float64))
	if count > retainCompleted+1 {
		t.Errorf("collection holds %d tasks, want at most %d", count, retainCompleted+1)
	}
	if resp, _ := get(t, srv.URL+CollectionURI+"/1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("oldest task still served: status %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+last); resp.StatusCode != http.StatusNoContent {
		t.Errorf("recent task monitor: status %d, want 204", resp.StatusCode)
	}
}

func TestTaskServiceAndSubTasks(t *testing.T) {
	m, srv := newTestServer(t)
	_, body := get(t, srv.URL+ServiceURI)
	if body["ServiceEnabled"] != true || body["Tasks"].(map[string]any)["@odata.id"] != CollectionURI {
		t.Errorf("TaskService = %v", body)
	}
	res := m.Start("Noop", func(context.Context, func(int)) error { return nil })
	id := res.TaskMonitor[len(MonitorURI)+1:]
	tests := []struct {
		path       string
		wantStatus int
	}{
		{CollectionURI + "/" + id + "/SubTasks", http.StatusOK},
		{CollectionURI + "/" + id + "/SubTasks/1", http.StatusNotFound},
		{CollectionURI + "/99/SubTasks", http.StatusNotFound},
		{MonitorURI + "/99", http.StatusNotFound},
		{CollectionURI + "/" + strconv.Itoa(99), http.StatusNotFound},
	}
	for _, tt := range tests {
		if resp, _ := get(t, srv.URL+tt.path); resp.StatusCode != tt.wantStatus {
			t.Errorf("GET %s: status %d, want %d", tt.path, resp.StatusCode, tt.wantStatus)
		}
	}
}
