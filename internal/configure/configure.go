package configure

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
	itls "github.com/bltn/hl7gw/internal/tls"
)

var (
	ErrInputClosed = errors.New("input ended before the wizard finished")
	ErrDeclined    = errors.New("exists and was not overwritten")
)

const (
	defaultMetricsPort = 8081
	defaultChannelPort = 2575
	defaultIdleTimeout = config.Duration(time.Minute)
	defaultMinVersion  = "1.2"
)

var tlsModes = []string{config.TLSModeOff, "mTLS"}

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

	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return err
	}

	p.printf("\n%s\n\nNext steps:\n", term.Success("Wrote "+path))
	if files := secretFiles(cfg); len(files) > 0 {
		p.printf("  Put each destination's bearer token or password in its file:\n")
		for _, file := range files {
			p.printf("    %s\n", file)
		}
	}
	if filepath.Clean(path) == config.DefaultPath {
		p.printf("  ./hl7gw run\n")
	} else {
		p.printf("  ./hl7gw run -config %s\n", path)
	}
	return nil
}

func collect(p *prompter, dir string) config.Config {
	var cfg config.Config

	taken := map[int]bool{}
	names := map[string]bool{}
	nextPort := defaultChannelPort
	for more := true; more; {
		done := p.section(fmt.Sprintf("Channel %d", len(cfg.Channels)+1))
		ch := channel(p, dir, taken, names, nextPort)
		cfg.Channels = append(cfg.Channels, ch)
		nextPort = ch.Source.Port + 1
		more = p.confirm("Add another channel?")
		done()
	}

	done := p.section("Metrics")
	cfg.Metrics.Port = p.askInt("Port", defaultMetricsPort, freePort(taken))
	done()

	p.printf("\n")
	cfg.DataDir = p.ask("Data directory", config.DefaultDataDir, nonEmpty)

	cfg.Log.Level = config.DefaultLogLevel
	cfg.Log.Format = config.LogFormatAuto
	cfg.CertReloadInterval = config.DefaultCertReloadInterval
	cfg.Retention = config.DefaultRetention
	return cfg
}

func channel(p *prompter, dir string, taken map[int]bool, names map[string]bool, defaultPort int) config.Channel {
	var ch config.Channel
	ch.Name = p.ask("Name (e.g. adt, lab-results)", "", func(s string) error {
		if err := config.ValidateChannelName(s); err != nil {
			return err
		}
		if names[s] {
			return fmt.Errorf("channel %s already exists", s)
		}
		return nil
	})
	names[ch.Name] = true
	ch.Source = source(p, taken, defaultPort)
	ch.Destination = destination(p, dir, ch.Name)
	return ch
}

func source(p *prompter, taken map[int]bool, defaultPort int) config.Source {
	defer p.section("Source")()
	var src config.Source
	src.Port = p.askInt("Listen port (MLLP)", defaultPort, freePort(taken))
	taken[src.Port] = true
	src.IdleTimeout = p.askDuration("Idle timeout", defaultIdleTimeout)
	src.MaxConnections = p.askInt("Max concurrent connections", config.DefaultMaxTCPConnections, positive)

	defer p.section("TLS")()
	src.TLS = &config.TLS{Mode: p.choose("Mode", tlsModes, config.TLSModeOff)}
	if src.TLS.Mode == config.TLSModeMTLS {
		src.TLS.CertFile = p.ask("Server certificate file", "", nonEmpty)
		src.TLS.KeyFile = p.ask("Server private key file", "", nonEmpty)
		src.TLS.ClientCAFile = p.ask("Client CA bundle file", "", nonEmpty)
		src.TLS.MinVersion = p.choose("Minimum TLS version", itls.Versions, defaultMinVersion)
	}
	return src
}

func destination(p *prompter, dir, name string) config.Destination {
	defer p.section("Destination")()
	var dst config.Destination
	p.ask("URL", "", func(s string) error { return dst.URL.UnmarshalText([]byte(s)) })

	defer p.section("Auth")()
	dst.Auth = &config.Auth{Type: p.choose("Type", config.AuthTypes, config.AuthBearer)}
	switch dst.Auth.Type {
	case config.AuthBearer:
		dst.Auth.TokenFile = p.ask("Token file", filepath.Join(dir, name+".token"), nonEmpty)
	case config.AuthBasic:
		dst.Auth.Username = p.ask("Username", "", config.ValidateUsername)
		dst.Auth.PasswordFile = p.ask("Password file", filepath.Join(dir, name+".password"), nonEmpty)
	}
	return dst
}

func freePort(taken map[int]bool) func(int) error {
	return func(n int) error {
		if err := config.ValidatePort(n); err != nil {
			return err
		}
		if taken[n] {
			return fmt.Errorf("port %d is already in use", n)
		}
		return nil
	}
}

func secretFiles(cfg config.Config) []string {
	var files []string
	for _, ch := range cfg.Channels {
		if file := ch.Destination.Auth.SecretFile(); file != "" && !slices.Contains(files, file) {
			files = append(files, file)
		}
	}
	return files
}
