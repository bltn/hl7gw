package configure

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
)

var (
	ErrInputClosed = errors.New("input ended before the wizard finished")
	ErrDeclined    = errors.New("exists and was not overwritten")
)

const (
	defaultAdminPort   = 8081
	defaultChannelPort = 2575
	defaultIdleTimeout = config.Duration(time.Minute)
	defaultMinVersion  = "1.2"
	maxPort            = 65535
	adminTokenName     = "admin.token"
	adminTokenBytes    = 32
)

var (
	logLevels   = []string{"debug", "info", "warn", "error"}
	logFormats  = []string{config.LogFormatAuto, config.LogFormatJSON, config.LogFormatText}
	tlsVersions = []string{"1.2", "1.3"}
)

func Run(path string, in io.Reader, out io.Writer, term console.Terminal) error {
	p := &prompter{in: bufio.NewReader(in), out: out, term: term}

	if _, err := os.Stat(path); err == nil {
		overwrite := p.confirm(path + " already exists. Overwrite?")
		if p.err != nil {
			return p.err
		}
		if !overwrite {
			return fmt.Errorf("%s: %w", path, ErrDeclined)
		}
	}

	term.Banner(out)
	p.printf("Writing %s\n", path)
	cfg := collect(p, filepath.Dir(path))
	if p.err != nil {
		return p.err
	}

	created, err := writeAdminToken(cfg.Admin.TokenFile)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return err
	}

	status := "generated"
	if !created {
		status = "already existed, left unchanged"
	}
	p.printf("\n%s\n\nNext steps:\n  Admin API token: %s (%s)\n  Put each destination's bearer token in its file:\n", term.Success("Wrote "+path), cfg.Admin.TokenFile, status)
	for _, file := range tokenFiles(cfg) {
		p.printf("    %s\n", file)
	}
	p.printf("  ./hl7gw run -config %s\n", path)
	return nil
}

func collect(p *prompter, dir string) config.Config {
	var cfg config.Config

	taken := map[int]bool{}
	names := map[string]bool{}
	nextPort := defaultChannelPort
	for {
		p.heading(fmt.Sprintf("Channel %d", len(cfg.Channels)+1))
		ch := channel(p, dir, taken, names, nextPort)
		cfg.Channels = append(cfg.Channels, ch)
		nextPort = ch.Source.Port + 1
		if !p.confirm("Add another channel?") {
			break
		}
	}

	p.heading("Admin API")
	cfg.Admin.Port = p.askInt("Admin port", defaultAdminPort, freePort(taken))
	cfg.Admin.TokenFile = filepath.Join(dir, adminTokenName)

	p.heading("General")
	cfg.DataDir = p.ask("Data directory", config.DefaultDataDir, nonEmpty)
	cfg.Log.Level = p.choose("Log level", logLevels, config.DefaultLogLevel)
	cfg.Log.Format = p.choose("Log format", logFormats, config.LogFormatAuto)

	cfg.CertReloadInterval = config.DefaultCertReloadInterval
	if slices.ContainsFunc(cfg.Channels, func(ch config.Channel) bool { return ch.Source.TLS.Mode == config.TLSModeMTLS }) {
		p.heading("TLS")
		cfg.CertReloadInterval = p.askDuration("Certificate reload interval", config.DefaultCertReloadInterval)
	}
	return cfg
}

func channel(p *prompter, dir string, taken map[int]bool, names map[string]bool, defaultPort int) config.Channel {
	var ch config.Channel

	ch.Name = p.ask("Channel name (e.g. adt, lab-results)", "", func(s string) error {
		if err := config.ValidateChannelName(s); err != nil {
			return err
		}
		if names[s] {
			return fmt.Errorf("channel %s already exists", s)
		}
		return nil
	})
	names[ch.Name] = true

	ch.Source.Port = p.askInt("Listen port (MLLP)", defaultPort, freePort(taken))
	taken[ch.Source.Port] = true
	ch.Source.IdleTimeout = p.askDuration("Idle timeout", defaultIdleTimeout)
	ch.Source.MaxConnections = p.askInt("Max concurrent connections", config.DefaultMaxTCPConnections, positive)

	t := &config.TLS{Mode: config.TLSModeOff}
	if p.confirm("Require client certificates (mTLS)?") {
		t.Mode = config.TLSModeMTLS
		t.CertFile = p.ask("Server certificate file", "", nonEmpty)
		t.KeyFile = p.ask("Server private key file", "", nonEmpty)
		t.ClientCAFile = p.ask("Client CA bundle file", "", nonEmpty)
		t.MinVersion = p.choose("Minimum TLS version", tlsVersions, defaultMinVersion)
	}
	ch.Source.TLS = t

	p.ask("Forward messages to (URL)", "", func(s string) error { return ch.Destination.URL.UnmarshalText([]byte(s)) })
	ch.Destination.Auth = &config.Auth{
		Type:      "bearer",
		TokenFile: p.ask("Bearer token file", filepath.Join(dir, ch.Name+".token"), nonEmpty),
	}
	return ch
}

func freePort(taken map[int]bool) func(int) error {
	return func(n int) error {
		if err := port(n); err != nil {
			return err
		}
		if taken[n] {
			return fmt.Errorf("port %d is already in use", n)
		}
		return nil
	}
}

func writeAdminToken(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304 -- path is derived from the operator-supplied output flag
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	token := make([]byte, adminTokenBytes)
	_, _ = rand.Read(token)
	_, writeErr := fmt.Fprintf(f, "%s\n", hex.EncodeToString(token))
	return true, errors.Join(writeErr, f.Close())
}

func tokenFiles(cfg config.Config) []string {
	var files []string
	for _, ch := range cfg.Channels {
		if !slices.Contains(files, ch.Destination.Auth.TokenFile) {
			files = append(files, ch.Destination.Auth.TokenFile)
		}
	}
	return files
}
