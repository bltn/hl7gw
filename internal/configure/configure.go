package configure

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
)

var (
	ErrInputClosed = errors.New("input ended before the wizard finished")
	ErrDeclined    = errors.New("exists and was not overwritten")
)

const (
	defaultAdminPort          = 8081
	defaultAdminTokenEnv      = "HL7GW_ADMIN_TOKEN" //nolint:gosec // G101 -- env var name, not a credential
	defaultChannelPort        = 2575
	defaultIdleTimeoutSeconds = 60
	defaultMinVersion         = "1.2"
	maxPort                   = 65535
)

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	logLevels      = []string{"debug", "info", "warn", "error"}
	logFormats     = []string{config.LogFormatAuto, config.LogFormatJSON, config.LogFormatText}
	tlsModes       = []string{config.TLSModeOff, config.TLSModeMTLS}
	tlsVersions    = []string{"1.2", "1.3"}
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
	cfg := collect(p)
	if p.err != nil {
		return p.err
	}

	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return err
	}

	p.printf("\n%s\n\nNext steps:\n", term.Success("Wrote "+path))
	for _, env := range tokenEnvs(cfg) {
		p.printf("  export %s=...\n", env)
	}
	p.printf("  ./hl7gw run -config %s\n", path)
	return nil
}

func collect(p *prompter) config.Config {
	var cfg config.Config

	p.heading("Admin API")
	cfg.AdminAPI.Port = p.askInt("Port", defaultAdminPort, port)
	cfg.AdminAPI.TokenEnv = p.ask("Token environment variable", defaultAdminTokenEnv, envName)

	p.heading("General")
	cfg.DataDir = p.ask("Data directory", config.DefaultDataDir, nonEmpty)
	cfg.Log.Level = p.choose("Log level", logLevels, config.DefaultLogLevel)
	cfg.Log.Format = p.choose("Log format", logFormats, config.LogFormatAuto)

	taken := map[int]bool{cfg.AdminAPI.Port: true}
	names := map[string]bool{}
	nextPort := defaultChannelPort
	for {
		p.heading(fmt.Sprintf("Channel %d", len(cfg.Channels)+1))
		ch := channel(p, taken, names, nextPort)
		cfg.Channels = append(cfg.Channels, ch)
		nextPort = ch.TCPListener.Port + 1
		if !p.confirm("Add another channel?") {
			break
		}
	}

	cfg.TLSReloadIntervalSeconds = config.DefaultTLSReloadIntervalSeconds
	if slices.ContainsFunc(cfg.Channels, func(ch config.Channel) bool { return ch.TCPListener.TLS.Mode == config.TLSModeMTLS }) {
		p.heading("TLS")
		cfg.TLSReloadIntervalSeconds = p.askInt("Certificate reload interval in seconds", config.DefaultTLSReloadIntervalSeconds, positive)
	}
	return cfg
}

func channel(p *prompter, taken map[int]bool, names map[string]bool, defaultPort int) config.Channel {
	var ch config.Channel

	ch.Name = p.ask("Name", "", func(s string) error {
		if err := config.ValidateChannelName(s); err != nil {
			return err
		}
		if names[s] {
			return fmt.Errorf("channel %s already exists", s)
		}
		return nil
	})
	names[ch.Name] = true

	ch.TCPListener.Port = p.askInt("MLLP port", defaultPort, func(n int) error {
		if err := port(n); err != nil {
			return err
		}
		if taken[n] {
			return fmt.Errorf("port %d is already in use", n)
		}
		return nil
	})
	taken[ch.TCPListener.Port] = true
	ch.TCPListener.IdleTimeoutSeconds = p.askInt("Idle timeout in seconds", defaultIdleTimeoutSeconds, positive)
	ch.TCPListener.MaxConnections = p.askInt("Max connections", config.DefaultMaxTCPConnections, positive)

	t := &config.TLS{Mode: p.choose("TLS mode", tlsModes, config.TLSModeOff)}
	if t.Mode == config.TLSModeMTLS {
		t.ServerCertPath = p.ask("Server certificate path", "", nonEmpty)
		t.ServerPKPath = p.ask("Server private key path", "", nonEmpty)
		t.ClientCAPath = p.ask("Client CA bundle path", "", nonEmpty)
		t.MinVersion = json.Number(p.choose("Minimum TLS version", tlsVersions, defaultMinVersion))
	}
	ch.TCPListener.TLS = t

	p.ask("Destination URL", "", func(s string) error { return ch.Destination.URL.UnmarshalText([]byte(s)) })
	ch.Destination.Auth = &config.Auth{
		Type:     "bearer",
		TokenEnv: p.ask("Destination token environment variable", tokenEnvDefault(ch.Name), envName),
	}
	return ch
}

func tokenEnvDefault(channel string) string {
	name := strings.ToUpper(strings.ReplaceAll(channel, "-", "_")) + "_API_TOKEN"
	if envName(name) != nil {
		return ""
	}
	return name
}

func tokenEnvs(cfg config.Config) []string {
	envs := []string{cfg.AdminAPI.TokenEnv}
	for _, ch := range cfg.Channels {
		if !slices.Contains(envs, ch.Destination.Auth.TokenEnv) {
			envs = append(envs, ch.Destination.Auth.TokenEnv)
		}
	}
	return envs
}
