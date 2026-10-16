//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/jobs"
	"github.com/hrtsegv/convoy/internal/testdb"
)

func TestJobsSurviveServerProcessRestart(t *testing.T) {
	dsn := testdb.New(t)
	setTestEnv(t)
	first := startServerProcess(t, dsn)
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Post(first.baseURL+"/v1/jobs", "application/json",
		strings.NewReader(`{"kind":"echo","payload":{"message":"persisted","number":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	var created jobs.Job
	err = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || err != nil || created.ID == "" {
		t.Fatal("server did not confirm job creation")
	}
	first.stop(t, true)
	second := startServerProcess(t, dsn)
	response, err = client.Get(second.baseURL + "/v1/jobs/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var read jobs.Job
	err = json.NewDecoder(response.Body).Decode(&read)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || err != nil || read.ID != created.ID || read.Kind != created.Kind ||
		read.Queue != "default" || read.State != "available" || !read.CreatedAt.Equal(created.CreatedAt) ||
		string(read.Payload) != string(created.Payload) {
		t.Fatal("job did not survive the API process being killed and restarted")
	}
	client.CloseIdleConnections()
	second.stop(t, false)
	t.Log("after 201, killed the API process; a new process read the same committed job and payload")
}

func TestServerProcessHelper(t *testing.T) {
	if os.Getenv("CONVOY_TEST_SERVER_PROCESS") == "1" {
		main()
	}
}

type serverProcess struct {
	command *exec.Cmd
	baseURL string
	done    chan struct{}
	waitErr error
	stopped bool
}

func startServerProcess(t *testing.T, dsn string) *serverProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestServerProcessHelper$")
	command.Env = append(os.Environ(),
		"CONVOY_TEST_SERVER_PROCESS=1", "CONVOY_DATABASE_URL="+dsn,
		"CONVOY_HTTP_ADDR=127.0.0.1:0", "CONVOY_LOG_LEVEL=INFO")
	command.Stdout = io.Discard
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal("could not capture server startup logs")
	}
	if err := command.Start(); err != nil {
		t.Fatal("could not start API subprocess")
	}
	process := &serverProcess{command: command, done: make(chan struct{})}
	t.Cleanup(func() { process.stop(t, true) })
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			var entry struct {
				Message string `json:"msg"`
				Address string `json:"address"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Message == "HTTP server listening" {
				address <- entry.Address
			}
		}
	}()
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	select {
	case addr := <-address:
		process.baseURL = "http://" + addr
	case <-process.done:
		t.Fatal("API subprocess exited before listening")
	case <-time.After(10 * time.Second):
		t.Fatal("API subprocess did not start before the test deadline")
	}
	return process
}

func (p *serverProcess) stop(t *testing.T, force bool) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if force {
		if err := p.command.Process.Kill(); err != nil {
			t.Error("could not kill API subprocess")
		}
	} else {
		if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Error("could not signal API subprocess")
		}
	}
	select {
	case <-p.done:
		var exitErr *exec.ExitError
		if force {
			if !errors.As(p.waitErr, &exitErr) {
				t.Error("expected forced API termination to return an exit error")
			}
		} else if p.waitErr != nil {
			t.Error("API process did not shut down successfully")
		}
	case <-time.After(5 * time.Second):
		p.command.Process.Kill()
		<-p.done
		t.Error("API process exceeded its shutdown deadline")
	}
}
