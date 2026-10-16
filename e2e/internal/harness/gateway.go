package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	apiToken        = "harness-admin-token"
	channelToken    = "harness-channel-token"
	AdminTokenEnv   = "HLGW_ADMIN_API_TOKEN" //nolint:gosec // G101 -- env var name, not a credential
	ChannelTokenEnv = "HARNESS_CHANNEL_TOKEN"

	TLSReloadInterval  = time.Second
	startupExitTimeout = 10 * time.Second
)

type Gateway struct {
	cmd          *exec.Cmd
	cfgPath      string
	workDir      string
	channelPorts map[string]int
	channelTLS   map[string]*ChannelTLS
	apiPort      int
	dataDir      string
	outBuf       *syncBuffer
	errBuf       *syncBuffer
	signalOnce   sync.Once
	stopOnce     sync.Once
	stopErr      error
}

func (gw *Gateway) DataDir() string {
	return gw.dataDir
}

func (gw *Gateway) APIURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", gw.apiPort)
}

func (gw *Gateway) APIToken() string {
	return apiToken
}

func (gw *Gateway) ChannelToken() string {
	return channelToken
}

func (gw *Gateway) DBPath(channel string) string {
	return filepath.Join(gw.dataDir, channel+".db")
}

type Channel struct {
	Name               string
	DestURL            string
	MaxTCPConns        int
	TCPIdleTimeoutSecs int
	TLS                *ChannelTLS
}

type ExitResult struct {
	Code   int
	Stdout string
	Stderr string
}

type Log struct {
	Level  string
	Format string
}

func StartGateway(t *testing.T, channels ...Channel) (*Gateway, error) {
	return StartGatewayWithLog(t, Log{}, channels...)
}

func StartGatewayWithLog(t *testing.T, log Log, channels ...Channel) (*Gateway, error) {
	ports, err := FreePorts(len(channels) + 1)
	if err != nil {
		return nil, fmt.Errorf("find free ports: %w", err)
	}
	channelPorts, apiPort := ports[:len(channels)], ports[len(channels)]

	workDir := t.TempDir()
	cfgPath, err := writeConfig(workDir, channels, channelPorts, apiPort, log)
	if err != nil {
		return nil, err
	}

	gw, err := spawnGateway(cfgPath, workDir, StartupEnv())
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = gw.Stop() })

	gw.cfgPath = cfgPath
	gw.workDir = workDir
	gw.dataDir = filepath.Join(workDir, "data")
	gw.channelPorts = make(map[string]int, len(channels))
	gw.channelTLS = make(map[string]*ChannelTLS, len(channels))
	for i, ch := range channels {
		gw.channelPorts[ch.Name] = channelPorts[i]
		gw.channelTLS[ch.Name] = ch.TLS
	}
	gw.apiPort = apiPort

	if err := gw.awaitListening(); err != nil {
		_ = gw.Stop()
		return nil, err
	}

	return gw, nil
}

func (gw *Gateway) Restart(t *testing.T) (*Gateway, error) {
	next, err := spawnGateway(gw.cfgPath, gw.workDir, StartupEnv())
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = next.Stop() })

	next.cfgPath = gw.cfgPath
	next.workDir = gw.workDir
	next.dataDir = gw.dataDir
	next.channelPorts = gw.channelPorts
	next.channelTLS = gw.channelTLS
	next.apiPort = gw.apiPort

	if err := next.awaitListening(); err != nil {
		_ = next.Stop()
		return nil, err
	}

	return next, nil
}

func (gw *Gateway) awaitListening() error {
	if !eventually(5*time.Second, func() bool {
		logs := gw.Logs()
		for name := range gw.channelPorts {
			if countLogs(logs, func(r LogRecord) bool { return r.Msg == "listening" && r.Attr("channel") == name }) == 0 {
				return false
			}
		}
		return countLogs(logs, func(r LogRecord) bool { return r.Msg == "admin api listening" }) > 0
	}) {
		return fmt.Errorf("gateway did not start listening within deadline")
	}
	return nil
}

func StartupEnv() []string {
	return Env(map[string]string{AdminTokenEnv: apiToken, ChannelTokenEnv: channelToken})
}

func Env(set map[string]string, unset ...string) []string {
	drop := make(map[string]bool, len(set)+len(unset))
	for k := range set {
		drop[k] = true
	}
	for _, k := range unset {
		drop[k] = true
	}

	env := make([]string, 0, len(os.Environ())+len(set))
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if !drop[key] {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}

func RunUntilExit(t *testing.T, config string, env []string) (ExitResult, error) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if config != "" {
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			return ExitResult{}, fmt.Errorf("write config: %w", err)
		}
	}

	gw, err := spawnGateway(configPath, "", env)
	if err != nil {
		return ExitResult{}, err
	}

	done := make(chan error, 1)
	go func() { done <- gw.cmd.Wait() }()

	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			return ExitResult{}, fmt.Errorf("wait for gateway: %w", err)
		}
		return ExitResult{
			Code:   gw.cmd.ProcessState.ExitCode(),
			Stdout: gw.outBuf.String(),
			Stderr: gw.errBuf.String(),
		}, nil
	case <-time.After(startupExitTimeout):
		_ = gw.cmd.Process.Kill()
		<-done
		return ExitResult{}, fmt.Errorf("gateway did not exit within %v", startupExitTimeout)
	}
}

func spawnGateway(configPath, dir string, env []string) (*Gateway, error) {
	binPath, err := builtBinary()
	if err != nil {
		return nil, fmt.Errorf("build gateway binary: %w", err)
	}

	outBuf := &syncBuffer{}
	errBuf := &syncBuffer{}
	//nolint:gosec,noctx // G204: our own freshly built test binary, test-controlled args; noctx: lifecycle is managed by the caller (SIGTERM in Stop, or kill after the exit timeout), not context cancellation (which would SIGKILL and lose coverage)
	cmd := exec.Command(binPath, "-config", configPath)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gateway: %w", err)
	}

	return &Gateway{cmd: cmd, outBuf: outBuf, errBuf: errBuf}, nil
}

func (gw *Gateway) StopNoWait() error {
	var err error
	gw.signalOnce.Do(func() { err = gw.cmd.Process.Signal(syscall.SIGTERM) })
	return err
}

func (gw *Gateway) Stop() error {
	gw.stopOnce.Do(func() {
		_ = gw.StopNoWait()
		gw.stopErr = gw.cmd.Wait()
	})
	return gw.stopErr
}

func (gw *Gateway) Kill() error {
	var err error
	gw.stopOnce.Do(func() {
		err = gw.cmd.Process.Kill()
		gw.stopErr = gw.cmd.Wait()
	})
	return err
}

func (gw *Gateway) Dial(channel string) (*Conn, error) {
	ct := gw.channelTLS[channel]
	if ct == nil {
		return gw.DialRaw(channel)
	}
	return gw.DialTLS(channel, ct.ClientLeaf)
}

func (gw *Gateway) DialTLS(channel string, leaf *Leaf) (*Conn, error) {
	ct := gw.channelTLS[channel]
	if ct == nil {
		return nil, fmt.Errorf("channel %q does not serve tls", channel)
	}

	conn, err := gw.DialRaw(channel)
	if err != nil {
		return nil, err
	}
	cfg, err := clientTLS(ct.CA, leaf)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	tlsConn, err := conn.startTLS(cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (gw *Gateway) DialRaw(channel string) (*Conn, error) {
	port, ok := gw.channelPorts[channel]
	if !ok {
		return nil, fmt.Errorf("no channel named %q", channel)
	}

	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	return &Conn{conn: conn, r: bufio.NewReader(conn)}, nil
}

func (gw *Gateway) SendAndAck(channel, msg string) (string, error) {
	conn, err := gw.Dial(channel)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(msg); err != nil {
		return "", err
	}
	return conn.ReadMessage()
}

type LogRecord struct {
	Level string
	Msg   string
	Attrs map[string]any
}

func (r LogRecord) Attr(key string) string {
	v, ok := r.Attrs[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

func ParseLogs(stdout string) []LogRecord {
	var records []LogRecord
	for _, line := range strings.Split(stdout, "\n") {
		if rec, ok := parseLogLine(line); ok {
			records = append(records, rec)
		}
	}
	return records
}

func parseLogLine(line string) (LogRecord, bool) {
	if strings.HasPrefix(line, "{") {
		return parseJSONLogLine(line)
	}
	return parseTextLogLine(line)
}

func parseJSONLogLine(line string) (LogRecord, bool) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		return LogRecord{}, false
	}
	rec := LogRecord{Attrs: fields}
	rec.Level, _ = fields["level"].(string)
	rec.Msg, _ = fields["msg"].(string)
	delete(fields, "time")
	delete(fields, "level")
	delete(fields, "msg")
	return rec, rec.Msg != ""
}

func parseTextLogLine(line string) (LogRecord, bool) {
	_, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
	if !ok {
		return LogRecord{}, false
	}
	level, rest, ok := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if !ok {
		return LogRecord{}, false
	}
	rec := LogRecord{Level: level, Attrs: map[string]any{}}
	rest = strings.TrimLeft(rest, " ")
	var msg []string
	for rest != "" {
		token, after, _ := strings.Cut(rest, " ")
		key, value, isAttr := strings.Cut(token, "=")
		if !isAttr || !isAttrKey(key) {
			msg = append(msg, token)
			rest = strings.TrimLeft(after, " ")
			continue
		}
		if strings.HasPrefix(value, `"`) {
			quoted := rest[len(key)+1:]
			end := closingQuote(quoted)
			if end < 0 {
				return LogRecord{}, false
			}
			unquoted, err := strconv.Unquote(quoted[:end+1])
			if err != nil {
				return LogRecord{}, false
			}
			value, after = unquoted, quoted[end+1:]
		}
		rec.Attrs[key] = value
		rest = strings.TrimLeft(after, " ")
	}
	rec.Msg = strings.Join(msg, " ")
	return rec, rec.Msg != ""
}

func isAttrKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r == '_', r == '.', r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func countLogs(records []LogRecord, pred func(LogRecord) bool) int {
	n := 0
	for _, r := range records {
		if pred(r) {
			n++
		}
	}
	return n
}

func (gw *Gateway) Logs() []LogRecord {
	return ParseLogs(gw.outBuf.String())
}

func (gw *Gateway) CountLogs(pred func(LogRecord) bool) int {
	return countLogs(gw.Logs(), pred)
}

func (gw *Gateway) WaitForLog(timeout time.Duration, pred func(LogRecord) bool) bool {
	return eventually(timeout, func() bool {
		return gw.CountLogs(pred) > 0
	})
}

func (gw *Gateway) Stdout() string {
	return gw.outBuf.String()
}

func (gw *Gateway) Stderr() string {
	return gw.errBuf.String()
}

func DeadDestURL() (string, error) {
	ports, err := FreePorts(1)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", ports[0]), nil
}

func FreePorts(n int) ([]int, error) {
	var lc net.ListenConfig
	ports := make([]int, 0, n)

	for range n {
		ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		defer func() { _ = ln.Close() }()
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}

	return ports, nil
}

func writeConfig(dir string, channels []Channel, channelPorts []int, apiPort int, log Log) (string, error) {
	entries := make([]map[string]any, 0, len(channels))
	for i, ch := range channels {
		tlsEntry := map[string]any{"mode": "off"}
		if ch.TLS != nil {
			tlsEntry = map[string]any{
				"mode":             "mtls",
				"client_ca_path":   ch.TLS.ClientCAPath,
				"server_cert_path": ch.TLS.ServerCertPath,
				"server_pk_path":   ch.TLS.ServerPKPath,
				"min_version":      json.Number(ch.TLS.MinVersion),
			}
		}
		listener := map[string]any{
			"port": channelPorts[i],
			"tls":  tlsEntry,
		}
		if ch.MaxTCPConns != 0 {
			listener["max_connections"] = ch.MaxTCPConns
		}
		idleSecs := ch.TCPIdleTimeoutSecs
		if idleSecs == 0 {
			idleSecs = 86400
		}
		listener["idle_timeout_seconds"] = idleSecs
		entry := map[string]any{
			"name":         ch.Name,
			"tcp_listener": listener,
			"destination": map[string]any{
				"url":  ch.DestURL,
				"auth": map[string]any{"type": "bearer", "token_env": ChannelTokenEnv},
			},
		}
		entries = append(entries, entry)
	}
	cfg := map[string]any{
		"tls_reload_interval_seconds": int(TLSReloadInterval / time.Second),
		"channels":                    entries,
		"admin_api":                   map[string]any{"port": apiPort, "token_env": AdminTokenEnv},
	}
	logEntry := map[string]any{}
	if log.Level != "" {
		logEntry["level"] = log.Level
	}
	if log.Format != "" {
		logEntry["format"] = log.Format
	}
	if len(logEntry) > 0 {
		cfg["log"] = logEntry
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}

	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return path, nil
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte{}, b.buf.Bytes()...)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
