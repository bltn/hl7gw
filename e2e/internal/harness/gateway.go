package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	channelToken     = "harness-channel-token"
	channelPassword  = "harness-channel-password"
	channelTokenName = "channel.token"

	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthBasic  = "basic"

	TLSReloadInterval  = time.Second
	startupExitTimeout = 10 * time.Second
)

type Gateway struct {
	cmd          *exec.Cmd
	cfgPath      string
	workDir      string
	channelPorts map[string]int
	channelTLS   map[string]*ChannelTLS
	metricsPort  int
	socketPath   string
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

func (gw *Gateway) MetricsURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", gw.metricsPort)
}

func (gw *Gateway) SocketPath() string {
	return gw.socketPath
}

func ShortTempDir(t *testing.T) (string, error) {
	dir, err := os.MkdirTemp("", "hl7gw")
	if err != nil {
		return "", fmt.Errorf("short temp dir: %w", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, nil
}

func (gw *Gateway) ChannelToken() string {
	return channelToken
}

func (gw *Gateway) ChannelTokenFile() string {
	return filepath.Join(gw.workDir, channelTokenName)
}

func (gw *Gateway) ChannelPasswordFile(channel string) string {
	return filepath.Join(gw.workDir, channel+".password")
}

func (gw *Gateway) DBPath(channel string) string {
	return filepath.Join(gw.dataDir, channel+".db")
}

type ChannelAuth struct {
	Type     string
	Username string
}

type Channel struct {
	Name        string
	DestURL     string
	MaxTCPConns int
	IdleTimeout time.Duration
	TLS         *ChannelTLS
	Auth        *ChannelAuth
}

type ExitResult struct {
	Code   int
	Signal syscall.Signal
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
	channelPorts, metricsPort := ports[:len(channels)], ports[len(channels)]
	socketDir, err := ShortTempDir(t)
	if err != nil {
		return nil, err
	}
	socketPath := filepath.Join(socketDir, "s")

	workDir := t.TempDir()
	cfgPath, err := writeConfig(workDir, channels, channelPorts, metricsPort, socketPath, log)
	if err != nil {
		return nil, err
	}

	gw := &Gateway{
		cfgPath:      cfgPath,
		workDir:      workDir,
		dataDir:      filepath.Join(workDir, "data"),
		channelPorts: make(map[string]int, len(channels)),
		channelTLS:   make(map[string]*ChannelTLS, len(channels)),
		metricsPort:  metricsPort,
		socketPath:   socketPath,
	}
	for i, ch := range channels {
		gw.channelPorts[ch.Name] = channelPorts[i]
		gw.channelTLS[ch.Name] = ch.TLS
	}
	return launch(t, gw)
}

func StartGatewayFromConfig(t *testing.T, workDir, cfgPath string, metricsPort int, channelPorts map[string]int) (*Gateway, error) {
	cfgDir := workDir
	if cfgPath != "" {
		cfgDir = filepath.Dir(cfgPath)
	}
	return launch(t, &Gateway{
		cfgPath:      cfgPath,
		workDir:      workDir,
		dataDir:      filepath.Join(cfgDir, "data"),
		channelPorts: channelPorts,
		channelTLS:   map[string]*ChannelTLS{},
		metricsPort:  metricsPort,
	})
}

func (gw *Gateway) Restart(t *testing.T) (*Gateway, error) {
	return launch(t, &Gateway{
		cfgPath:      gw.cfgPath,
		workDir:      gw.workDir,
		dataDir:      gw.dataDir,
		channelPorts: gw.channelPorts,
		channelTLS:   gw.channelTLS,
		metricsPort:  gw.metricsPort,
		socketPath:   gw.socketPath,
	})
}

func (gw *Gateway) Run(stdin, subcommand string, args ...string) (ExitResult, error) {
	p, err := gw.Start(strings.NewReader(stdin), subcommand, args...)
	if err != nil {
		return ExitResult{}, err
	}
	return p.Wait()
}

func (gw *Gateway) Start(stdin io.Reader, subcommand string, args ...string) (*Proc, error) {
	full := []string{subcommand}
	if gw.cfgPath != "" {
		full = append(full, "-config", gw.cfgPath)
	}
	return Start(gw.workDir, stdin, append(full, args...)...)
}

func launch(t *testing.T, gw *Gateway) (*Gateway, error) {
	args := []string{"run"}
	if gw.cfgPath != "" {
		args = append(args, "-config", gw.cfgPath)
	}
	if err := gw.spawn(nil, args...); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = gw.Stop() })

	if err := gw.awaitListening(); err != nil {
		_ = gw.Stop()
		return nil, err
	}
	return gw, nil
}

func (gw *Gateway) awaitListening() error {
	if !eventually(5*time.Second, func() bool {
		logs := gw.Logs()
		for name := range gw.channelPorts {
			if countLogs(logs, func(r LogRecord) bool { return r.Msg == "listening" && r.Attr("channel") == name }) == 0 {
				return false
			}
		}
		return countLogs(logs, func(r LogRecord) bool { return r.Msg == "ops listening" }) > 0 &&
			countLogs(logs, func(r LogRecord) bool { return r.Msg == "metrics listening" }) > 0
	}) {
		return fmt.Errorf("gateway did not start listening within deadline")
	}
	return nil
}

func RunUntilExit(dir, stdin string, args ...string) (ExitResult, error) {
	p, err := Start(dir, strings.NewReader(stdin), args...)
	if err != nil {
		return ExitResult{}, err
	}
	return p.Wait()
}

type Proc struct {
	cmd  *exec.Cmd
	out  *syncBuffer
	err  *syncBuffer
	done chan error
}

func Start(dir string, stdin io.Reader, args ...string) (*Proc, error) {
	gw := &Gateway{workDir: dir}
	if err := gw.spawn(stdin, args...); err != nil {
		return nil, err
	}
	p := &Proc{cmd: gw.cmd, out: gw.outBuf, err: gw.errBuf, done: make(chan error, 1)}
	go func() { p.done <- p.cmd.Wait() }()
	return p, nil
}

func (p *Proc) WaitForOutput(substr string, timeout time.Duration) bool {
	return eventually(timeout, func() bool { return strings.Contains(p.out.String(), substr) })
}

func (p *Proc) Signal(sig os.Signal) error {
	return p.cmd.Process.Signal(sig)
}

func (p *Proc) Wait() (ExitResult, error) {
	select {
	case err := <-p.done:
		if _, exited := errors.AsType[*exec.ExitError](err); err != nil && !exited {
			return ExitResult{}, fmt.Errorf("wait for process: %w", err)
		}
		res := ExitResult{
			Code:   p.cmd.ProcessState.ExitCode(),
			Stdout: p.out.String(),
			Stderr: p.err.String(),
		}
		if ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = ws.Signal()
		}
		return res, nil
	case <-time.After(startupExitTimeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		return ExitResult{}, fmt.Errorf("process did not exit within %v", startupExitTimeout)
	}
}

func (gw *Gateway) spawn(stdin io.Reader, args ...string) error {
	binPath, err := builtBinary()
	if err != nil {
		return fmt.Errorf("build gateway binary: %w", err)
	}

	gw.outBuf = &syncBuffer{}
	gw.errBuf = &syncBuffer{}
	//nolint:gosec,noctx // G204: our own freshly built test binary, test-controlled args; noctx: lifecycle is managed by the caller (SIGTERM in Stop, or kill after the exit timeout), not context cancellation (which would SIGKILL and lose coverage)
	gw.cmd = exec.Command(binPath, args...)
	gw.cmd.Dir = gw.workDir
	gw.cmd.Stdin = stdin
	gw.cmd.Stdout = gw.outBuf
	gw.cmd.Stderr = gw.errBuf

	if err := gw.cmd.Start(); err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	return nil
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

func writeConfig(dir string, channels []Channel, channelPorts []int, metricsPort int, socketPath string, log Log) (string, error) {
	channelTokenPath := filepath.Join(dir, channelTokenName)
	if err := os.WriteFile(channelTokenPath, []byte(channelToken+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}

	entries := make([]map[string]any, 0, len(channels))
	for i, ch := range channels {
		tlsEntry := map[string]any{"mode": "off"}
		if ch.TLS != nil {
			tlsEntry = map[string]any{
				"mode":           "mtls",
				"cert_file":      ch.TLS.CertFile,
				"key_file":       ch.TLS.KeyFile,
				"client_ca_file": ch.TLS.ClientCAFile,
				"min_version":    ch.TLS.MinVersion,
			}
		}
		source := map[string]any{
			"port": channelPorts[i],
			"tls":  tlsEntry,
		}
		if ch.MaxTCPConns != 0 {
			source["max_connections"] = ch.MaxTCPConns
		}
		idle := ch.IdleTimeout
		if idle == 0 {
			idle = 24 * time.Hour
		}
		source["idle_timeout"] = idle.String()
		chAuth := &ChannelAuth{Type: AuthBearer}
		if ch.Auth != nil {
			chAuth = ch.Auth
		}
		auth := map[string]any{"type": chAuth.Type}
		switch chAuth.Type {
		case AuthBearer:
			auth["token_file"] = channelTokenPath
		case AuthBasic:
			passwordPath := filepath.Join(dir, ch.Name+".password")
			if err := os.WriteFile(passwordPath, []byte(channelPassword+"\n"), 0o600); err != nil {
				return "", fmt.Errorf("write password file: %w", err)
			}
			auth["username"] = chAuth.Username
			auth["password_file"] = passwordPath
		}
		entry := map[string]any{
			"name":   ch.Name,
			"source": source,
			"destination": map[string]any{
				"url":  ch.DestURL,
				"auth": auth,
			},
		}
		entries = append(entries, entry)
	}
	cfg := map[string]any{
		"cert_reload_interval": TLSReloadInterval.String(),
		"channels":             entries,
		"metrics":              map[string]any{"port": metricsPort},
		"socket":               socketPath,
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
