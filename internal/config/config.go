package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bltn/hl7gw/internal/console"
	itls "github.com/bltn/hl7gw/internal/tls"
)

const (
	DefaultMaxTCPConnections  = 64
	DefaultDataDir            = "data"
	DefaultCertReloadInterval = Duration(2 * time.Minute)

	TLSModeOff  = "off"
	TLSModeMTLS = "mtls"

	DefaultLogLevel = "info"
	LogFormatAuto   = "auto"
	LogFormatJSON   = "json"
	LogFormatText   = "text"
)

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

type Duration time.Duration

func (d Duration) String() string {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

type Log struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

func (l Log) Logger(w io.Writer, term console.Terminal) *slog.Logger {
	level := logLevels[l.Level]
	if l.Format == LogFormatJSON || (l.Format == LogFormatAuto && !term.TTY) {
		return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	}
	return slog.New(term.Handler(w, level))
}

type Admin struct {
	Port      int    `json:"port"`
	TokenFile string `json:"token_file"`
	token     string
}

func (a *Admin) Token() string {
	return a.token
}

type Auth struct {
	Type      string `json:"type"`
	TokenFile string `json:"token_file"`
	token     string
}

func (a *Auth) Token() string {
	return a.token
}

type Destination struct {
	URL  DestURL `json:"url"`
	Auth *Auth   `json:"auth"`
}

type Channel struct {
	Name        string      `json:"name"`
	Source      Source      `json:"source"`
	Destination Destination `json:"destination"`
}

type Source struct {
	MaxConnections int      `json:"max_connections"`
	Port           int      `json:"port"`
	IdleTimeout    Duration `json:"idle_timeout"`
	TLS            *TLS     `json:"tls"`
}

type TLS struct {
	Mode          string `json:"mode"`
	CertFile      string `json:"cert_file,omitempty"`
	KeyFile       string `json:"key_file,omitempty"`
	ClientCAFile  string `json:"client_ca_file,omitempty"`
	MinVersion    string `json:"min_version,omitempty"`
	serving       atomic.Pointer[tls.Config]
	serverCert    tls.Certificate
	clientCAs     *x509.CertPool
	clientCACerts []*x509.Certificate
	minVersion    uint16
}

func (t *TLS) ServerConfig() *tls.Config {
	if t.Mode != TLSModeMTLS {
		return nil
	}
	return &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return t.serving.Load(), nil },
		MinVersion:         t.minVersion,
	}
}

func (t *TLS) load() error {
	if t.Mode != TLSModeMTLS {
		return nil
	}
	cert, err := itls.LoadServerCertAndKey(t.CertFile, t.KeyFile)
	if err != nil {
		return err
	}
	pool, certs, err := itls.LoadCAPool(t.ClientCAFile)
	if err != nil {
		return err
	}
	minVersion, err := itls.LoadMinimumVersion(t.MinVersion)
	if err != nil {
		return err
	}
	t.serverCert, t.clientCAs, t.clientCACerts, t.minVersion = cert, pool, certs, minVersion
	t.serving.Store(t.innerConfig())
	return nil
}

type DestURL struct{ *url.URL }

func (d *DestURL) UnmarshalText(text []byte) error {
	u, err := url.Parse(string(text))
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("destination.url must be an absolute http(s) URL, got %q", text)
	}
	d.URL = u
	return nil
}

func (d DestURL) MarshalText() ([]byte, error) {
	if d.URL == nil {
		return []byte{}, nil
	}
	return []byte(d.String()), nil
}

type Config struct {
	Admin              Admin     `json:"admin"`
	DataDir            string    `json:"data_dir"`
	CertReloadInterval Duration  `json:"cert_reload_interval"`
	Log                Log       `json:"log"`
	Channels           []Channel `json:"channels"`
}

var channelNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func ValidateChannelName(name string) error {
	if !channelNamePattern.MatchString(name) {
		return fmt.Errorf("channel name %q must be non-empty and contain only letters, digits, _ or -", name)
	}
	return nil
}

func Load(path string) (Config, error) {
	cfg, err := loadAndDecode(path)
	if err != nil {
		return Config{}, err
	}
	applyDefaults(&cfg)

	err = validate(cfg)
	if err != nil {
		return Config{}, err
	}

	adminToken, err := readToken(cfg.Admin.TokenFile)
	if err != nil {
		return Config{}, fmt.Errorf("admin: %w", err)
	}
	cfg.Admin.token = adminToken

	for _, ch := range cfg.Channels {
		if err := ch.Source.TLS.load(); err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		token, err := readToken(ch.Destination.Auth.TokenFile)
		if err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		ch.Destination.Auth.token = token
	}

	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.CertReloadInterval == 0 {
		cfg.CertReloadInterval = DefaultCertReloadInterval
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = DefaultLogLevel
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = LogFormatAuto
	}
	for i := range cfg.Channels {
		if cfg.Channels[i].Source.MaxConnections == 0 {
			cfg.Channels[i].Source.MaxConnections = DefaultMaxTCPConnections
		}
	}
}

func validate(cfg Config) error {
	if cfg.Admin.Port <= 0 {
		return fmt.Errorf("admin.port is required and must be positive, got %d", cfg.Admin.Port)
	}
	if cfg.Admin.TokenFile == "" {
		return errors.New("admin.token_file is required and must be non-empty")
	}
	if cfg.CertReloadInterval < 0 {
		return fmt.Errorf("cert_reload_interval must be positive, got %s", cfg.CertReloadInterval)
	}
	if _, ok := logLevels[cfg.Log.Level]; !ok {
		return fmt.Errorf("log.level must be one of \"debug\", \"info\", \"warn\", \"error\", got %q", cfg.Log.Level)
	}
	if cfg.Log.Format != LogFormatAuto && cfg.Log.Format != LogFormatJSON && cfg.Log.Format != LogFormatText {
		return fmt.Errorf("log.format must be %q, %q or %q, got %q", LogFormatAuto, LogFormatJSON, LogFormatText, cfg.Log.Format)
	}

	names := make(map[string]bool)
	ports := make(map[int]bool)
	for _, ch := range cfg.Channels {
		if err := ValidateChannelName(ch.Name); err != nil {
			return err
		}
		if names[ch.Name] {
			return fmt.Errorf("duplicate channel name %s", ch.Name)
		}
		names[ch.Name] = true
		if ch.Source.Port == cfg.Admin.Port {
			return fmt.Errorf("channel %s: source.port %d collides with admin.port", ch.Name, ch.Source.Port)
		}
		if ports[ch.Source.Port] {
			return fmt.Errorf("channel %s: duplicate source.port %d", ch.Name, ch.Source.Port)
		}
		ports[ch.Source.Port] = true
		if ch.Source.MaxConnections < 0 {
			return fmt.Errorf("channel %s: source.max_connections must be positive, got %d", ch.Name, ch.Source.MaxConnections)
		}
		if ch.Source.IdleTimeout <= 0 {
			return fmt.Errorf("channel %s: source.idle_timeout is required and must be positive, got %s", ch.Name, ch.Source.IdleTimeout)
		}
		if err := validateTLS(ch.Source.TLS); err != nil {
			return fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		if ch.Destination.URL.URL == nil {
			return fmt.Errorf("channel %s: destination.url is required", ch.Name)
		}
		if ch.Destination.Auth == nil {
			return fmt.Errorf("channel %s: destination.auth is required", ch.Name)
		}
		if ch.Destination.Auth.Type != "bearer" {
			return fmt.Errorf("channel %s: destination.auth.type must be \"bearer\", got %q", ch.Name, ch.Destination.Auth.Type)
		}
		if ch.Destination.Auth.TokenFile == "" {
			return fmt.Errorf("channel %s: destination.auth.token_file is required and must be non-empty", ch.Name)
		}
	}
	return nil
}

func validateTLS(t *TLS) error {
	if t == nil {
		return errors.New("source.tls is required")
	}
	if t.Mode != TLSModeOff && t.Mode != TLSModeMTLS {
		return fmt.Errorf("source.tls.mode must be %q or %q, got %q", TLSModeOff, TLSModeMTLS, t.Mode)
	}
	if t.Mode == TLSModeOff {
		return nil
	}
	if t.CertFile == "" {
		return errors.New("source.tls.cert_file is required and must be non-empty")
	}
	if t.KeyFile == "" {
		return errors.New("source.tls.key_file is required and must be non-empty")
	}
	if t.ClientCAFile == "" {
		return errors.New("source.tls.client_ca_file is required and must be non-empty")
	}
	if t.MinVersion == "" {
		return errors.New("source.tls.min_version is required")
	}
	return nil
}

func loadAndDecode(path string) (Config, error) {
	configFile, err := os.Open(path) //nolint:gosec // G304 -- path is an operator-supplied config flag, no traversal boundary
	if err != nil {
		return Config{}, fmt.Errorf("opening config file: %w", err)
	}
	defer func() { _ = configFile.Close() }()

	var config Config
	decoder := json.NewDecoder(configFile)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parsing config file: %w", err)
	}

	return config, nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304 -- path is an operator-supplied config value, no traversal boundary
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return token, nil
}
