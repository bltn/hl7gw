//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	errorPrefix             = "error:"
	parsingConfigFileSuffix = "parsing config file"
	metricsOK               = `"metrics": {"port": 9990}`
	addrInUse               = "address already in use"
	noSuchFile              = "no such file or directory"
	tlsOffOK                = `"tls": {"mode": "off"}`
	sourceOK                = `"source": {"port": 9999, "idle_timeout": "24h", ` + tlsOffOK + `}`
	source9998OK            = `"source": {"port": 9998, "idle_timeout": "24h", ` + tlsOffOK + `}`
	destinationOK           = `"destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer", "token_file": "channel.token"}}`
)

func expectStartupFailure(t *testing.T, config string, wantStderr ...string) {
	t.Helper()
	dir := shortDir(t)
	configPath := filepath.Join(dir, "config.json")
	if config != "" {
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "channel.token"), []byte("e2e-channel-token\n"), 0o600); err != nil {
		t.Fatalf("write channel.token: %v", err)
	}
	res, err := harness.RunUntilExit(dir, "", "run", flagConfig, configPath)
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
			wantStderr: []string{errorPrefix, "opening config file", noSuchFile},
		},
		{
			name:       "unknown log level",
			body:       `{` + metricsOK + `, "log": {"level": "loud"}, "channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `log.level must be "debug", "info", "warn" or "error", got "loud"`},
		},
		{
			name:       "unknown log format",
			body:       `{` + metricsOK + `, "log": {"format": "xml"}, "channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `log.format must be "auto", "json" or "text", got "xml"`},
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
			name:       "pre-rename tcp_listener key",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "tcp_listener": {"port": 9999, "idle_timeout_seconds": 86400, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `unknown field "tcp_listener"`},
		},
		{
			name:       "pre-rename admin_api key",
			body:       `{"admin_api": {"port": 9990, "token_env": "X"}, "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `unknown field "admin_api"`},
		},
		{
			name:       "negative max_connections",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "max_connections": -1, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "source.max_connections must be positive"},
		},
		{
			name:       "negative cert_reload_interval",
			body:       `{` + metricsOK + `, "cert_reload_interval": "-1s", "channels": []}`,
			wantStderr: []string{errorPrefix, "cert_reload_interval must be positive, got -1s"},
		},
		{
			name:       "retention without a unit",
			body:       `{` + metricsOK + `, "retention": "30", "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `retention must be "off" or a whole number of days such as "30d", got "30"`},
		},
		{
			name:       "retention as a number",
			body:       `{` + metricsOK + `, "retention": 30, "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "cannot unmarshal number into Go struct field Config.retention of type config.Retention"},
		},
		{
			name:       "zero retention",
			body:       `{` + metricsOK + `, "retention": "0d", "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `retention must be "off" or a whole number of days such as "30d", got "0d"`},
		},
		{
			name:       "negative retention",
			body:       `{` + metricsOK + `, "retention": "-1d", "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `retention must be "off" or a whole number of days such as "30d", got "-1d"`},
		},
		{
			name:       "retention in words",
			body:       `{` + metricsOK + `, "retention": "forever", "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `retention must be "off" or a whole number of days such as "30d", got "forever"`},
		},
		{
			name:       "negative idle_timeout",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": "-5s", ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: source.idle_timeout is required and must be positive, got -5s"},
		},
		{
			name:       "missing idle_timeout",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: source.idle_timeout is required and must be positive, got 0s"},
		},
		{
			name:       "idle_timeout as a number",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": 60, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "cannot unmarshal number into Go struct field Config.channels.0.source.idle_timeout of type config.Duration"},
		},
		{
			name:       "idle_timeout without a unit",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": "60", ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `missing unit in duration "60"`},
		},
		{
			name:       "missing tls",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": "24h"}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: source.tls is required"},
		},
		{
			name:       "missing tls mode",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": "24h", "tls": {}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `channel bad: source.tls.mode must be "off" or "mtls", got ""`},
		},
		{
			name:       "garbage tls mode",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": "24h", "tls": {"mode": "tls1.2"}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `channel bad: source.tls.mode must be "off" or "mtls", got "tls1.2"`},
		},
		{
			name:       "tls min_version of the wrong json type",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 9999, "idle_timeout": "24h", "tls": {"mode": "mtls", "cert_file": "cert.pem", "key_file": "key.pem", "client_ca_file": "ca.pem", "min_version": true}}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "cannot unmarshal bool into Go struct field Config.channels.0.source.tls.min_version of type string"},
		},
		{
			name:       "channel name unsafe as filename",
			body:       `{` + metricsOK + `, "channels": [{"name": "a/b", "source": {"port": 9999, ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, `channel name "a/b" must be non-empty and contain only letters, digits, _ or -`},
		},
		{
			name:       "duplicate channel names",
			body:       `{` + metricsOK + `, "channels": [{"name": "twin", ` + source9998OK + `, ` + destinationOK + `}, {"name": "twin", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "duplicate channel name twin"},
		},
		{
			name:       "duplicate source ports",
			body:       `{` + metricsOK + `, "channels": [{"name": "first", ` + sourceOK + `, ` + destinationOK + `}, {"name": "second", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel second: duplicate source.port 9999"},
		},
		{
			name:       "negative metrics port",
			body:       `{"metrics": {"port": -1}, "channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "metrics.port must be a port between 1 and 65535, got -1"},
		},
		{
			name:       "metrics port too high",
			body:       `{"metrics": {"port": 70000}, "channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "metrics.port must be a port between 1 and 65535, got 70000"},
		},
		{
			name:       "missing metrics",
			body:       `{"channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "metrics.port must be a port between 1 and 65535, got 0"},
		},
		{
			name:       "missing metrics port",
			body:       `{"metrics": {}, "channels": []}`,
			wantStderr: []string{errorPrefix, "metrics.port must be a port between 1 and 65535, got 0"},
		},
		{
			name:       "pre-rename admin key",
			body:       `{"admin": {"port": 9990, "token_file": "admin.token"}, "channels": []}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `unknown field "admin"`},
		},
		{
			name:       "missing source port",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"idle_timeout": "24h", ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: source.port must be a port between 1 and 65535, got 0"},
		},
		{
			name:       "source port too high",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", "source": {"port": 70000, "idle_timeout": "24h", ` + tlsOffOK + `}, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: source.port must be a port between 1 and 65535, got 70000"},
		},
		{
			name:       "channel port collides with metrics port",
			body:       `{"metrics": {"port": 9999}, "channels": [{"name": "solo", ` + sourceOK + `, ` + destinationOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel solo: source.port 9999 collides with metrics.port"},
		},
		{
			name:       "unparseable destination url",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "://not-a-url"}}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "missing protocol scheme"},
		},
		{
			name:       "relative destination url",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "not-a-url"}}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, `destination.url must be an absolute http(s) URL, got "not-a-url"`},
		},
		{
			name:       "non-http destination url scheme",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "ftp://127.0.0.1/ingest"}}]}`,
			wantStderr: []string{errorPrefix, parsingConfigFileSuffix, "destination.url must be an absolute http(s) URL"},
		},
		{
			name:       "missing destination",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.url is required"},
		},
		{
			name:       "missing destination url",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"auth": {"type": "bearer", "token_file": "channel.token"}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.url is required"},
		},
		{
			name:       "missing destination auth",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/"}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth is required"},
		},
		{
			name:       "missing auth type",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"token_file": "channel.token"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.type must be "none", "bearer" or "basic", got ""`},
		},
		{
			name:       "unknown auth type",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "digest", "token_file": "channel.token"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.type must be "none", "bearer" or "basic", got "digest"`},
		},
		{
			name:       "basic missing username",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "basic", "password_file": "channel.password"}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth.username is required and must be non-empty"},
		},
		{
			name:       "basic username with colon",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "basic", "username": "svc:1", "password_file": "channel.password"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.username must not contain ":", got "svc:1"`},
		},
		{
			name:       "none with token_file",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "none", "token_file": "channel.token"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.token_file is only valid when type is "bearer"`},
		},
		{
			name:       "basic with token_file",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "basic", "username": "svc", "password_file": "channel.password", "token_file": "channel.token"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.token_file is only valid when type is "bearer"`},
		},
		{
			name:       "bearer with username",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer", "token_file": "channel.token", "username": "svc"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.username is only valid when type is "basic"`},
		},
		{
			name:       "none with password_file",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "none", "password_file": "channel.password"}}}]}`,
			wantStderr: []string{errorPrefix, `channel bad: destination.auth.password_file is only valid when type is "basic"`},
		},
		{
			name:       "basic missing password_file",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "basic", "username": "svc"}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth.password_file is required and must be non-empty"},
		},
		{
			name:       "missing auth token_file",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer"}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth.token_file is required and must be non-empty"},
		},
		{
			name:       "empty auth token_file",
			body:       `{` + metricsOK + `, "channels": [{"name": "bad", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": {"type": "bearer", "token_file": ""}}}]}`,
			wantStderr: []string{errorPrefix, "channel bad: destination.auth.token_file is required and must be non-empty"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStartupFailure(t, tc.body, tc.wantStderr...)
		})
	}
}

func TestRunWithoutConfigFlagNeedsDefaultFile(t *testing.T) {
	t.Parallel()
	res, err := harness.RunUntilExit(t.TempDir(), "", "run")
	if err != nil {
		t.Fatalf("run gateway: %v", err)
	}
	if res.Code != 1 {
		t.Errorf("exit code = %d, want 1", res.Code)
	}
	for _, want := range []string{errorPrefix, "./hl7gw.json", flagConfig} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, res.Stderr)
		}
	}
	if res.Stdout != "" {
		t.Errorf("unexpected stdout\n--- stdout ---\n%s", res.Stdout)
	}
}

func TestExitsWhenSecretFileUnusable(t *testing.T) {
	t.Parallel()
	channel := func(auth string) string {
		return `"channels": [{"name": "solo", ` + sourceOK + `, "destination": {"url": "http://127.0.0.1:1/", "auth": ` + auth + `}}]`
	}
	bearer := func(tokenFile string) string { return `{"type": "bearer", "token_file": ` + tokenFile + `}` }
	basic := func(passwordFile string) string {
		return `{"type": "basic", "username": "svc", "password_file": ` + passwordFile + `}`
	}
	tests := []struct {
		name       string
		body       string
		wantStderr []string
	}{
		{"channel token file missing", `{` + metricsOK + `, ` + channel(bearer(`"missing.token"`)) + `}`, []string{"channel solo: reading token file: open /", "/missing.token: no such file or directory"}},
		{"channel token file empty", `{` + metricsOK + `, ` + channel(bearer(`"/dev/null"`)) + `}`, []string{"channel solo: token file /dev/null is empty"}},
		{"channel password file missing", `{` + metricsOK + `, ` + channel(basic(`"missing.password"`)) + `}`, []string{"channel solo: reading password file: open /", "/missing.password: no such file or directory"}},
		{"channel password file empty", `{` + metricsOK + `, ` + channel(basic(`"/dev/null"`)) + `}`, []string{"channel solo: password file /dev/null is empty"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectStartupFailure(t, tc.body, append([]string{errorPrefix}, tc.wantStderr...)...)
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
	freePort, metricsPort := ports[0], ports[1]
	socket := shortSocket(t)

	cfg := fmt.Sprintf(`{"data_dir": %q, "socket": %q, "metrics": {"port": %d}, "channels": [
		{"name": "first", "source": {"port": %d, "idle_timeout": "24h", `+tlsOffOK+`}, `+destinationOK+`},
		{"name": "second", "source": {"port": %d, "idle_timeout": "24h", `+tlsOffOK+`}, `+destinationOK+`}
	]}`, t.TempDir(), socket, metricsPort, freePort, blockedPort)

	expectStartupFailure(t, cfg, errorPrefix, "channel second", addrInUse)

	expectPortClosed(t, freePort)
	expectNoSocket(t, socket)
}

func TestExitsWhenMetricsPortUnavailable(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	blocker, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatalf("bind blocker port: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	socket := shortSocket(t)
	cfg := fmt.Sprintf(`{"data_dir": %q, "socket": %q, "metrics": {"port": %d}, "channels": [
		{"name": "solo", `+sourceOK+`, `+destinationOK+`}
	]}`, t.TempDir(), socket, blockedPort)
	expectStartupFailure(t, cfg, errorPrefix, "binding metrics listener", addrInUse)
	expectNoSocket(t, socket)
}

func TestUnwindsMetricsListenerOnChannelPortCollision(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	blocker, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatalf("bind blocker port: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	ports, err := harness.FreePorts(1)
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	metricsPort := ports[0]
	socket := shortSocket(t)

	cfg := fmt.Sprintf(`{"data_dir": %q, "socket": %q, "metrics": {"port": %d}, "channels": [
		{"name": "solo", "source": {"port": %d, "idle_timeout": "24h", `+tlsOffOK+`}, `+destinationOK+`}
	]}`, t.TempDir(), socket, metricsPort, blockedPort)
	expectStartupFailure(t, cfg, errorPrefix, "channel solo", addrInUse)

	expectPortClosed(t, metricsPort)
	expectNoSocket(t, socket)
}

func expectPortClosed(t *testing.T, port int) {
	t.Helper()
	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		_ = conn.Close()
		t.Errorf("port %d still accepts connections after the gateway exited", port)
	}
}
