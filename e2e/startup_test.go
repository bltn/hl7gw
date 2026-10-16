//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	errorPrefix             = "error:"
	parsingConfigFileSuffix = "parsing config file"
	adminAPIOK              = `"admin_api": {"port": 9990, "token_env": "` + harness.AdminTokenEnv + `"}`
	tlsOffOK                = `"tls": {"mode": "off"}`
	listenerOK              = `"tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400, ` + tlsOffOK + `}`
	listener9998OK          = `"tcp_listener": {"port": 9998, "idle_timeout_seconds": 86400, ` + tlsOffOK + `}`
	destinationOK           = `"destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer", "token_env": "` + harness.ChannelTokenEnv + `"}}`
)

func expectStartupFailure(t *testing.T, config string, env []string, wantStderr ...string) {
	t.Helper()
	res, err := harness.RunUntilExit(t, config, env)
	if err != nil {
		t.Fatalf("run gateway: %v", err)
	}

	if res.Code != 1 {
		t.Errorf("exit code = %d, want 1", res.Code)
	}
	for _, want := range wantStderr {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, res.Stderr)
		}
	}
	for _, r := range harness.ParseLogs(res.Stdout) {
		if r.Msg == msgListening {
			t.Errorf("gateway started listening despite the fatal startup error\n--- stdout ---\n%s", res.Stdout)
		}
	}
}

func TestExitsOnInvalidConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantStderr []string
	}{
		{
			name:       "missing file",
			wantStderr: []string{errorPrefix, "opening config file", "no such file or directory"},
		},
		{
			name:       "unknown log level",
			body:       `{` + adminAPIOK + `, "log": {"level": "loud"}, "channels": [{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `log.level must be one of "debug", "info", "warn", "error", got "loud"`},
		},
		{
			name:       "unknown log format",
			body:       `{` + adminAPIOK + `, "log": {"format": "xml"}, "channels": [{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `log.format must be "json" or "text", got "xml"`},
		},
		{
			name:       "truncated json",
			body:       `{"channels": [`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix},
		},
		{
			name:       "unknown field",
			body:       `{"channels": [], "bogus": true}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `unknown field "bogus"`},
		},
		{
			name:       "flat pre-tcp_listener channel shape",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "listener_port": 9999, "tcp_idle_timeout_seconds": 86400, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `unknown field "listener_port"`},
		},
		{
			name:       "negative max_connections",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "max_connections": -1, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "tcp_listener.max_connections must be positive"},
		},
		{
			name:       "negative tls_reload_interval_seconds",
			body:       `{` + adminAPIOK + `, "tls_reload_interval_seconds": -1, "channels": []}`,
			wantStderr: []string{errorPrefix, "tls_reload_interval_seconds must be positive, got -1"},
		},
		{
			name:       "negative idle_timeout_seconds",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": -5, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: tcp_listener.idle_timeout_seconds is required and must be positive, got -5"},
		},
		{
			name:       "missing idle_timeout_seconds",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: tcp_listener.idle_timeout_seconds is required and must be positive, got 0"},
		},
		{
			name:       "missing tls",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: tcp_listener.tls is required"},
		},
		{
			name:       "missing tls mode",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400, "tls": {}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `channel bad: tcp_listener.tls.mode must be "off" or "mtls", got ""`},
		},
		{
			name:       "garbage tls mode",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400, "tls": {"mode": "tls1.2"}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `channel bad: tcp_listener.tls.mode must be "off" or "mtls", got "tls1.2"`},
		},
		{
			name:       "tls min_version of the wrong json type",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400, "tls": {"mode": "mtls", "client_ca_path": "ca.pem", "server_cert_path": "cert.pem", "server_pk_path": "key.pem", "min_version": true}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "cannot unmarshal bool into Go value of type json.Number"},
		},
		{
			name:       "tls min_version quoted garbage",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400, "tls": {"mode": "mtls", "client_ca_path": "ca.pem", "server_cert_path": "cert.pem", "server_pk_path": "key.pem", "min_version": "potato"}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `cannot unmarshal string "potato" into Go value of type json.Number`},
		},
		{
			name:       "channel name unsafe as filename",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "a/b", "tcp_listener": {"port": 9999, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `channel name "a/b" must be non-empty and contain only letters, digits, _ or -`},
		},
		{
			name:       "duplicate channel names",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "twin", ` + listener9998OK + `, ` + destinationOK + `}, {"name": "twin", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "duplicate channel name twin"},
		},
		{
			name:       "duplicate listener ports",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "first", ` + listenerOK + `, ` + destinationOK + `}, {"name": "second", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel second: duplicate tcp_listener.port 9999"},
		},
		{
			name:       "negative admin_api port",
			body:       `{"admin_api": {"port": -1, "token_env": "` + harness.AdminTokenEnv + `"}, "channels": [{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "admin_api.port is required and must be positive, got -1"},
		},
		{
			name:       "missing admin_api",
			body:       `{"channels": [{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "admin_api.port is required and must be positive, got 0"},
		},
		{
			name:       "missing admin_api port",
			body:       `{"admin_api": {"token_env": "` + harness.AdminTokenEnv + `"}, "channels": []}`,
			wantStderr: []string{errorPrefix, "admin_api.port is required and must be positive, got 0"},
		},
		{
			name:       "missing admin_api token_env",
			body:       `{"admin_api": {"port": 9990}, "channels": []}`,
			wantStderr: []string{errorPrefix, "admin_api.token_env is required and must be non-empty"},
		},
		{
			name:       "empty admin_api token_env",
			body:       `{"admin_api": {"port": 9990, "token_env": ""}, "channels": []}`,
			wantStderr: []string{errorPrefix, "admin_api.token_env is required and must be non-empty"},
		},
		{
			name:       "channel port collides with admin port",
			body:       `{"admin_api": {"port": 9999, "token_env": "` + harness.AdminTokenEnv + `"}, "channels": [{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel solo: tcp_listener.port 9999 collides with admin_api.port"},
		},
		{
			name:       "unparseable destination url",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "://not-a-url"}}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "missing protocol scheme"},
		},
		{
			name:       "relative destination url",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "not-a-url"}}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `destination.url must be an absolute http(s) URL, got "not-a-url"`},
		},
		{
			name:       "non-http destination url scheme",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "ftp://127.0.0.1/ingest"}}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "destination.url must be an absolute http(s) URL"},
		},
		{
			name:       "missing destination",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.url is required"},
		},
		{
			name:       "missing destination url",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"auth": {"type": "bearer", "token_env": "` + harness.ChannelTokenEnv + `"}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.url is required"},
		},
		{
			name:       "missing destination auth",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "http://127.0.0.1:1/"}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth is required"},
		},
		{
			name:       "missing auth type",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"token_env": "` + harness.ChannelTokenEnv + `"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.type must be "bearer", got ""`},
		},
		{
			name:       "unknown auth type",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "basic", "token_env": "` + harness.ChannelTokenEnv + `"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.type must be "bearer", got "basic"`},
		},
		{
			name:       "missing auth token_env",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer"}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth.token_env is required and must be non-empty"},
		},
		{
			name:       "empty auth token_env",
			body:       `{` + adminAPIOK + `, "channels": [{"name": "bad", ` + listenerOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer", "token_env": ""}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth.token_env is required and must be non-empty"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStartupFailure(t, tc.body, harness.StartupEnv(), tc.wantStderr...)
		})
	}
}

func TestExitsWhenAdminTokenUnresolvable(t *testing.T) {
	t.Parallel()
	cfg := `{"admin_api": {"port": 9990, "token_env": "E2E_ADMIN_TOKEN"}, "channels": [
		{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}
	]}`

	tests := []struct {
		name string
		env  []string
	}{
		{name: "unset", env: harness.Env(map[string]string{harness.ChannelTokenEnv: "e2e-channel-token"}, "E2E_ADMIN_TOKEN")},
		{name: "set but empty", env: harness.Env(map[string]string{harness.ChannelTokenEnv: "e2e-channel-token", "E2E_ADMIN_TOKEN": ""})},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStartupFailure(t, cfg, tc.env, "E2E_ADMIN_TOKEN must be set to a non-empty bearer token")
		})
	}
}

func TestExitsWhenChannelTokenUnresolvable(t *testing.T) {
	t.Parallel()
	cfg := `{` + adminAPIOK + `, "channels": [
		{"name": "solo", ` + listenerOK + `, ` + destinationOK + `}
	]}`

	tests := []struct {
		name string
		env  []string
	}{
		{name: "unset", env: harness.Env(map[string]string{harness.AdminTokenEnv: "e2e-admin-token"}, harness.ChannelTokenEnv)},
		{name: "set but empty", env: harness.Env(map[string]string{harness.AdminTokenEnv: "e2e-admin-token", harness.ChannelTokenEnv: ""})},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStartupFailure(t, cfg, tc.env, "channel solo: "+harness.ChannelTokenEnv+" must be set to a non-empty bearer token")
		})
	}
}

func TestUnwindsListenersOnPortCollision(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	blocker, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatalf("bind blocker port: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	ports, err := harness.FreePorts(2)
	if err != nil {
		t.Fatalf("find free ports: %v", err)
	}
	freePort, apiPort := ports[0], ports[1]

	cfg := fmt.Sprintf(`{"data_dir": %q, "admin_api": {"port": %d, "token_env": "`+harness.AdminTokenEnv+`"}, "channels": [
		{"name": "first", "tcp_listener": {"port": %d, "idle_timeout_seconds": 86400, `+tlsOffOK+`}, `+destinationOK+`},
		{"name": "second", "tcp_listener": {"port": %d, "idle_timeout_seconds": 86400, `+tlsOffOK+`}, `+destinationOK+`}
	]}`, t.TempDir(), apiPort, freePort, blockedPort)

	expectStartupFailure(t, cfg, harness.StartupEnv(), errorPrefix, "channel second", "address already in use")

	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", freePort))
	if err == nil {
		_ = conn.Close()
		t.Errorf("port %d still accepts connections after the gateway exited", freePort)
	}
}
