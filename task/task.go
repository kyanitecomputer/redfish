// Package task runs long-running operations as Redfish tasks. It serves the
// TaskService, the task collection and members, and the task monitors, and
// turns an operation into the 202 Accepted response of an asynchronous action
// as described in DSP0266.
package task

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"src.kyanite.computer/redfish/gen"
	"src.kyanite.computer/redfish/odata"
	"src.kyanite.computer/redfish/server"
)

// URIs of the resources the manager serves. Task monitor URIs are chosen by the
// service; this package serves them under MonitorURI.
const (
	ServiceURI    = "/redfish/v1/TaskService"
	CollectionURI = ServiceURI + "/Tasks"
	MonitorURI    = ServiceURI + "/TaskMonitors"
)

// retainCompleted bounds how many finished tasks are kept; the oldest is
// dropped first, matching CompletedTaskOverWritePolicy "Oldest".
const retainCompleted = 32

// Func is an operation run as a task. It reports its progress in percent
// through progress. Returning a *server.Error (or wrapping one) chooses the
// error response a client reads from the task monitor.
type Func func(ctx context.Context, progress func(percent int)) error

// Manager tracks tasks and implements the generated TaskService, Task, and
// TaskCollection providers.
type Manager struct {
	mu     sync.Mutex
	tasks  map[string]*entry
	order  []string // task ids, oldest first
	nextID uint64
}

type entry struct {
	task gen.Task
	done bool
	err  error
}

// New returns an empty Manager.
func New() *Manager {
	return &Manager{tasks: map[string]*entry{}}
}

// Register wires the TaskService, the task collection and members, and the
// task monitors into mux.
func (m *Manager) Register(mux *http.ServeMux) {
	gen.RegisterTaskService(mux, m)
	gen.RegisterTaskCollection(mux, m)
	gen.RegisterTask(mux, m)
	server.Handle(mux, "GET", MonitorURI+"/{TaskId}", m.serveMonitor)
}

// Start runs fn in its own goroutine as a new task called name and returns the
// action result announcing it: 202 Accepted with the task monitor and the
// Task resource.
func (m *Manager) Start(name string, fn Func) *server.ActionResult {
	m.mu.Lock()
	m.nextID++
	id := strconv.FormatUint(m.nextID, 10)
	e := &entry{task: gen.Task{
		ODataID:         CollectionURI + "/" + id,
		Id:              id,
		Name:            name,
		TaskState:       gen.TaskTaskStateRunning,
		TaskStatus:      gen.ResourceHealthOK,
		StartTime:       now(),
		TaskMonitor:     MonitorURI + "/" + id,
		PercentComplete: new(int64),
	}}
	m.tasks[id] = e
	m.order = append(m.order, id)
	m.prune()
	t := e.task
	m.mu.Unlock()

	go m.run(e, fn)
	return &server.ActionResult{TaskMonitor: t.TaskMonitor, Body: &t}
}

func (m *Manager) run(e *entry, fn Func) {
	err := fn(context.Background(), func(percent int) {
		m.mu.Lock()
		pc := int64(min(max(percent, 0), 100))
		e.task.PercentComplete = &pc
		m.mu.Unlock()
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	e.done, e.err = true, err
	e.task.EndTime = now()
	if err == nil {
		pc := int64(100)
		e.task.PercentComplete = &pc
		e.task.TaskState = gen.TaskTaskStateCompleted
		e.task.Messages = []gen.MessageMessage{{
			MessageId:       "TaskEvent.1.0.TaskCompletedOK",
			Message:         "The task with Id '" + e.task.Id + "' has completed.",
			MessageArgs:     []string{e.task.Id},
			MessageSeverity: gen.ResourceHealthOK,
		}}
		return
	}
	e.task.TaskState = gen.TaskTaskStateException
	e.task.TaskStatus = gen.ResourceHealthCritical
	id, msg := odata.MsgGeneralError, err.Error()
	if se, ok := errors.AsType[*server.Error](err); ok {
		id, msg = se.MessageID, se.Message
	}
	e.task.Messages = []gen.MessageMessage{
		{
			MessageId:       "TaskEvent.1.0.TaskAborted",
			Message:         "The task with Id '" + e.task.Id + "' has been aborted.",
			MessageArgs:     []string{e.task.Id},
			MessageSeverity: gen.ResourceHealthCritical,
		},
		{MessageId: id, Message: msg, MessageSeverity: gen.ResourceHealthCritical},
	}
}

// prune drops the oldest finished tasks beyond retainCompleted. The caller
// holds m.mu.
func (m *Manager) prune() {
	done := 0
	for _, id := range m.order {
		if m.tasks[id].done {
			done++
		}
	}
	kept := m.order[:0]
	for _, id := range m.order {
		if done > retainCompleted && m.tasks[id].done {
			delete(m.tasks, id)
			done--
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
}

// serveMonitor answers a GET on a task monitor: 202 Accepted with the Task while
// the operation runs, then the operation's final response.
func (m *Manager) serveMonitor(w http.ResponseWriter, r *http.Request, p server.Params) {
	m.mu.Lock()
	e, ok := m.tasks[p.Get("TaskId")]
	var t gen.Task
	var done bool
	var err error
	if ok {
		t, done, err = e.task, e.done, e.err
	}
	m.mu.Unlock()

	switch {
	case !ok:
		server.NotFound(w, r)
	case !done:
		server.WriteActionResult(w, &server.ActionResult{TaskMonitor: t.TaskMonitor, Body: &t})
	case err != nil:
		server.WriteErr(w, err)
	default:
		server.NoContent(w)
	}
}

// GetTaskService implements gen.TaskServiceProvider.
func (m *Manager) GetTaskService(_ context.Context) (*gen.TaskService, error) {
	enabled, events := true, false
	return &gen.TaskService{
		Id:                              "TaskService",
		Name:                            "Task Service",
		ServiceEnabled:                  &enabled,
		CompletedTaskOverWritePolicy:    gen.TaskServiceOverWritePolicyOldest,
		LifeCycleEventOnTaskStateChange: &events,
		DateTime:                        now(),
		Tasks:                           odata.Link(CollectionURI),
	}, nil
}

// PatchTaskService implements gen.TaskServiceProvider. No property is writable.
func (m *Manager) PatchTaskService(ctx context.Context, patch *gen.TaskServicePatch) (*gen.TaskService, error) {
	switch {
	case patch.ServiceEnabled != nil:
		return nil, server.NotWritable("ServiceEnabled")
	case patch.TaskAutoDeleteTimeoutMinutes != nil:
		return nil, server.NotWritable("TaskAutoDeleteTimeoutMinutes")
	case patch.TaskMonitorAutoExpirySeconds != nil:
		return nil, server.NotWritable("TaskMonitorAutoExpirySeconds")
	}
	return m.GetTaskService(ctx)
}

// ListTask implements gen.TaskCollectionProvider. Tasks have no subtasks.
func (m *Manager) ListTask(_ context.Context, p server.Params) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if parent := p.Get("TaskId"); parent != "" {
		if _, ok := m.tasks[parent]; !ok {
			return nil, &server.Error{
				Status:    http.StatusNotFound,
				MessageID: odata.MsgResourceNotFound,
				Message:   "The resource at URI '" + p.Path() + "' was not found.",
			}
		}
		return []string{}, nil
	}
	ids := make([]string, len(m.order))
	for i, id := range m.order {
		ids[i] = CollectionURI + "/" + id
	}
	return ids, nil
}

// GetTask implements gen.TaskProvider.
func (m *Manager) GetTask(_ context.Context, p server.Params) (*gen.Task, error) {
	if p.Get("TaskId2") != "" {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[p.Get("TaskId")]
	if !ok {
		return nil, nil
	}
	t := e.task
	return &t, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
