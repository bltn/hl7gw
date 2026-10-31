//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	msgOpsListening    = "ops listening"
	msgOpsRequest      = "ops request"
	cannotReachGateway = "cannot reach the gateway at"
	soloSocketConfig   = `{"socket": %q, ` + metricsOK + `, "channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`
)

func shortSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortDir(t), "s")
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := harness.ShortTempDir(t)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func expectNoSocket(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lstat %s = %v, want not exist after the gateway exited", path, err)
	}
}

func TestSocketRefusesSecondInstance(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	res := run(t, gw, "", "run")
	if res.Code != 1 {
		t.Errorf("second instance exit code = %d, want 1\n--- stderr ---\n%s", res.Code, res.Stderr)
	}
	for _, want := range []string{errorPrefix, "another hl7gw is already serving", gw.SocketPath()} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, res.Stderr)
		}
	}
	if summaries := listMessages(t, gw, channelName); len(summaries) != 0 {
		t.Errorf("first instance should still answer with no messages, got %+v", summaries)
	}
}

func TestSocketRefusesNonSocketFile(t *testing.T) {
	t.Parallel()
	socket := shortSocket(t)
	if err := os.WriteFile(socket, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectStartupFailure(t, fmt.Sprintf(soloSocketConfig, socket), errorPrefix, socket+" exists and is not a socket")
	if data, err := os.ReadFile(socket); err != nil || string(data) != "keep me" { //nolint:gosec // G304 -- test temp path
		t.Errorf("file at the socket path = %q, %v; want it untouched", data, err)
	}
}

func TestSocketReportsBindFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(filepath.Dir(shortSocket(t)), "missing", "s")
	expectStartupFailure(t, fmt.Sprintf(soloSocketConfig, missing), errorPrefix, "binding socket", noSuchFile)
}

func TestSocketRefusesLongPath(t *testing.T) {
	t.Parallel()
	long := "/tmp/" + strings.Repeat("x", 120)
	expectStartupFailure(t, fmt.Sprintf(soloSocketConfig, long), errorPrefix, "is too long", `set "socket" to a shorter path`)
}

func TestSocketFileMode(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	fi, err := os.Lstat(gw.SocketPath())
	if err != nil {
		t.Fatalf("lstat socket: %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o660 {
		t.Errorf("socket mode = %v, want a socket with 0660", fi.Mode())
	}
	if err := gw.Stop(); err != nil {
		t.Errorf("gateway exited uncleanly: %v", err)
	}
	expectNoSocket(t, gw.SocketPath())
}

func TestSocketLogsRequests(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	sendControlIDs(t, gw, channelName, "MSG00001")
	runOK(t, gw, "", subcommandReplay, channelName, "1")
	expectFailure(t, replay(t, gw, 999), 1, errorPrefix, "no message with id 999")

	if n := gw.CountLogs(withAttr(isMsg(msgOpsListening), "socket", gw.SocketPath())); n != 1 {
		t.Errorf("%q records naming the socket = %d, want 1\n--- logs ---\n%v", msgOpsListening, n, gw.Logs())
	}
	replayed := func(status, query string) logPred {
		return func(r harness.LogRecord) bool {
			return r.Msg == msgOpsRequest && r.Attr("method") == http.MethodPost && r.Attr("path") == "/replay" &&
				r.Attr("status") == status && strings.Contains(r.Attr("query"), query) && r.Attr("duration_ms") != ""
		}
	}
	if n := gw.CountLogs(replayed("200", "id=1")); n != 1 {
		t.Errorf("successful replay request records = %d, want 1\n--- logs ---\n%v", n, gw.Logs())
	}
	if n := gw.CountLogs(replayed("404", "id=999")); n != 1 {
		t.Errorf("failed replay request records = %d, want 1\n--- logs ---\n%v", n, gw.Logs())
	}
}

func TestSocketValidatesRequests(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"missing channel", http.MethodGet, "/messages", http.StatusBadRequest, "channel query parameter is required"},
		{"unknown channel", http.MethodGet, "/messages?channel=nope", http.StatusNotFound, noChannelNamedNope},
		{"get on unknown channel", http.MethodGet, "/messages/1?channel=nope", http.StatusNotFound, noChannelNamedNope},
		{"limit below one", http.MethodGet, "/messages?channel=e2e&limit=0", http.StatusBadRequest, `limit must be an integer of at least 1, got "0"`},
		{"limit above the cap", http.MethodGet, "/messages?channel=e2e&limit=501", http.StatusBadRequest, "limit must be at most 500, got 501"},
		{"negative before", http.MethodGet, "/messages?channel=e2e&before=-1", http.StatusBadRequest, `before must be an integer of at least 0, got "-1"`},
		{nonIntegerIDCase, http.MethodGet, "/messages/abc?channel=e2e", http.StatusBadRequest, badIDAbc},
		{"replay without id", http.MethodPost, "/replay?channel=e2e", http.StatusBadRequest, `id must be an integer of at least 1, got ""`},
		{"empty process body", http.MethodPost, "/process?channel=e2e", http.StatusBadRequest, "the message is empty"},
		{"unknown route", http.MethodGet, "/nope", http.StatusNotFound, ""},
		{"wrong method", http.MethodPost, "/messages?channel=e2e", http.StatusMethodNotAllowed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := gw.SocketDo(tc.method, tc.path, nil)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			if resp.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d; body %s", resp.Status, tc.wantStatus, resp.Body)
			}
			if !strings.Contains(string(resp.Body), tc.wantBody) {
				t.Errorf("body = %q, want it to contain %q", resp.Body, tc.wantBody)
			}
		})
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestProcessRejectsOversizedMessage(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	file := messageFile(t, harness.GiantMessage(16<<20+1024))
	expectFailure(t, run(t, gw, "", subcommandProcess, channelName, file), 1, errorPrefix, "reading the message", "request body too large")
	if rows := readMessageRows(t, gw); len(rows) != 0 {
		t.Errorf("persisted messages = %d, want none for a rejected message", len(rows))
	}
	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}
