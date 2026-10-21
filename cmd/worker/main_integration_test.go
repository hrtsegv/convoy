//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/jobs"
	"github.com/hrtsegv/convoy/internal/testdb"
)

func TestWorkerProcessRestartExperiment(t *testing.T) {
	setTestEnv(t)
	dsn := testdb.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	serverBinary := filepath.Join(t.TempDir(), "server")
	build := exec.CommandContext(ctx, "go", "build", "-o", serverBinary, "../server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("could not build API for process experiment: %s", output)
	}
	startAPI := func() (*testProcess, string) {
		process := startProcess(t, dsn, serverBinary)
		return process, "http://" + process.event(t, ctx, "HTTP server listening").Address
	}
	startWorker := func() *testProcess {
		process := startProcess(t, dsn, os.Args[0], "-test.run=^TestWorkerProcessHelper$")
		process.event(t, ctx, "worker started")
		return process
	}
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	api, address := startAPI()
	pending := make(map[string]bool)
	for i := 0; i < 100; i++ {
		job := postJob(t, client, address, fmt.Sprintf(`{"kind":"noop","payload":{"number":%d}}`, i))
		pending[job.ID] = true
	}
	if len(pending) != 100 {
		t.Fatal("enqueue did not create 100 distinct jobs")
	}
	client.CloseIdleConnections()
	api.stop(t, false)
	api, address = startAPI()
	process := startWorker()
	for i := 0; i < 100; i++ {
		event := process.event(t, ctx, "job succeeded")
		if !pending[event.JobID] {
			t.Fatal("worker completed an unexpected or duplicate job")
		}
		delete(pending, event.JobID)
		job := getJob(t, client, address, event.JobID)
		if job.State != "succeeded" || job.CompletedAt == nil {
			t.Fatal("completed job was not inspectable after API restart")
		}
	}
	process.stop(t, false)
	t.Log("enqueued 100 jobs, restarted the API, processed all 100, and inspected every succeeded job through HTTP")

	// A long handler gives the API time to restart while the worker owns a job.
	stranded := postJob(t, client, address, `{"kind":"sleep","payload":{"duration":"1m"}}`)
	next := postJob(t, client, address, `{"kind":"noop","payload":{}}`)
	process = startWorker()
	if event := process.event(t, ctx, "job claimed"); event.JobID != stranded.ID {
		t.Fatal("worker did not claim the oldest available job")
	}
	client.CloseIdleConnections()
	api.stop(t, false)
	_, address = startAPI()
	if job := getJob(t, client, address, stranded.ID); job.State != "running" {
		t.Fatal("API restart changed the worker's durable running state")
	}
	process.stop(t, true)
	process = startWorker()
	if event := process.event(t, ctx, "job succeeded"); event.JobID != next.ID {
		t.Fatal("restarted worker must process the available job without reclaiming the running job")
	}
	process.stop(t, false)
	if job := getJob(t, client, address, stranded.ID); job.State != "running" || job.CompletedAt != nil {
		t.Fatal("worker crash must leave its unfinished job running for inspection")
	}
	t.Log("restarted the API during a long job, killed the worker, and verified that a new worker left that job running while completing the next available job")
}

func TestWorkerProcessHelper(t *testing.T) {
	if os.Getenv("CONVOY_TEST_WORKER_PROCESS") == "1" {
		main()
	}
}

type processEvent struct {
	Message string `json:"msg"`
	Address string `json:"address"`
	JobID   string `json:"job_id"`
}

type testProcess struct {
	command *exec.Cmd
	events  chan processEvent
	done    chan struct{}
	err     error
	stopped bool
}

func startProcess(t *testing.T, dsn, executable string, args ...string) *testProcess {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Env = append(os.Environ(), "CONVOY_TEST_WORKER_PROCESS=1", "CONVOY_DATABASE_URL="+dsn,
		"CONVOY_HTTP_ADDR=127.0.0.1:0", "CONVOY_LOG_LEVEL=INFO")
	command.Stdout = io.Discard
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal("could not capture subprocess logs")
	}
	if err := command.Start(); err != nil {
		t.Fatal("could not start subprocess")
	}
	process := &testProcess{command: command, events: make(chan processEvent, 512), done: make(chan struct{})}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			var event processEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				process.events <- event
			}
		}
		process.err = errors.Join(scanner.Err(), command.Wait())
		close(process.events)
		close(process.done)
	}()
	t.Cleanup(func() { process.stop(t, false) })
	return process
}

func (p *testProcess) event(t *testing.T, ctx context.Context, message string) processEvent {
	t.Helper()
	for {
		select {
		case event, ok := <-p.events:
			if !ok {
				t.Fatalf("subprocess exited before %q", message)
			}
			if event.Message == message {
				return event
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q", message)
		}
	}
}

func (p *testProcess) stop(t *testing.T, force bool) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if force {
		p.command.Process.Kill()
	} else {
		p.command.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-p.done:
		var exitErr *exec.ExitError
		if force && !errors.As(p.err, &exitErr) {
			t.Error("expected killed subprocess to report an exit error")
		} else if !force && p.err != nil {
			t.Error("subprocess did not stop successfully")
		}
	case <-time.After(5 * time.Second):
		p.command.Process.Kill()
		<-p.done
		t.Error("subprocess exceeded its shutdown deadline")
	}
}

func postJob(t *testing.T, client *http.Client, address, body string) jobs.Job {
	t.Helper()
	response, err := client.Post(address+"/v1/jobs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal("could not enqueue through API")
	}
	defer response.Body.Close()
	var job jobs.Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil || response.StatusCode != http.StatusCreated || job.ID == "" {
		t.Fatal("API did not confirm job creation")
	}
	io.Copy(io.Discard, response.Body)
	return job
}

func getJob(t *testing.T, client *http.Client, address, id string) jobs.Job {
	t.Helper()
	response, err := client.Get(address + "/v1/jobs/" + id)
	if err != nil {
		t.Fatal("could not read job through API")
	}
	defer response.Body.Close()
	var job jobs.Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil || response.StatusCode != http.StatusOK || job.ID != id {
		t.Fatal("API did not return expected job")
	}
	io.Copy(io.Discard, response.Body)
	return job
}
