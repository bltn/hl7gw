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
	"sync/atomic"

	"github.com/bltn/hl7gw/internal/console"
	itls "github.com/bltn/hl7gw/internal/tls"
)

const (
	DefaultMaxTCPConnections        = 64
	DefaultDataDir                  = "data"
	DefaultTLSReloadIntervalSeconds = 120

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

type AdminAPI struct {
	Port     int    `json:"port"`
	TokenEnv string `json:"token_env"`
	token    string
}

func (a *AdminAPI) Token() string {
	return a.token
}

type Auth struct {
	Type     string `json:"type"`
	TokenEnv string `json:"token_env"`
	token    string
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
	TCPListener TCPListener `json:"tcp_listener"`
	Destination Destination `json:"destination"`
}

type TCPListener struct {
	MaxConnections     int  `json:"max_connections"`
	Port               int  `json:"port"`
	IdleTimeoutSeconds int  `json:"idle_timeout_seconds"`
	TLS                *TLS `json:"tls"`
}

type TLS struct {
	Mode           string      `json:"mode"`
	ClientCAPath   string      `json:"client_ca_path"`
	ServerCertPath string      `json:"server_cert_path"`
	ServerPKPath   string      `json:"server_pk_path"`
	MinVersion     json.Number `json:"min_version"`
	serving        atomic.Pointer[tls.Config]
	serverCert     tls.Certificate
	clientCAs      *x509.CertPool
	clientCACerts  []*x509.Certificate
	minVersion     uint16
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
	cert, err := itls.LoadServerCertAndKey(t.ServerCertPath, t.ServerPKPath)
	if err != nil {
		return err
	}
	pool, certs, err := itls.LoadCAPool(t.ClientCAPath)
	if err != nil {
		return err
	}
	minVersion, err := itls.LoadMinimumVersion(t.MinVersion.String())
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

type Config struct {
	DataDir                  string    `json:"data_dir"`
	TLSReloadIntervalSeconds int       `json:"tls_reload_interval_seconds"`
	Log                      Log       `json:"log"`
	AdminAPI                 AdminAPI  `json:"admin_api"`
	Channels                 []Channel `json:"channels"`
}

var channelNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func Load(path string) (Config, error) {
	cfg, err := loadAndDecode(path)
	if err != nil {
		return Config{}, err
	}

	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.TLSReloadIntervalSeconds == 0 {
		cfg.TLSReloadIntervalSeconds = DefaultTLSReloadIntervalSeconds
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = DefaultLogLevel
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = LogFormatAuto
	}

	err = validate(cfg)
	if err != nil {
		return Config{}, err
	}

	adminToken, err := resolveToken(cfg.AdminAPI.TokenEnv)
	if err != nil {
		return Config{}, err
	}
	cfg.AdminAPI.token = adminToken

	for _, ch := range cfg.Channels {
		if err := ch.TCPListener.TLS.load(); err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		token, err := resolveToken(ch.Destination.Auth.TokenEnv)
		if err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		ch.Destination.Auth.token = token
	}

	return cfg, nil
}

func validate(cfg Config) error {
	if cfg.AdminAPI.Port <= 0 {
		return fmt.Errorf("admin_api.port is required and must be positive, got %d", cfg.AdminAPI.Port)
	}
	if cfg.AdminAPI.TokenEnv == "" {
		return errors.New("admin_api.token_env is required and must be non-empty")
	}
	if cfg.TLSReloadIntervalSeconds < 0 {
		return fmt.Errorf("tls_reload_interval_seconds must be positive, got %d", cfg.TLSReloadIntervalSeconds)
	}
	if _, ok := logLevels[cfg.Log.Level]; !ok {
		return fmt.Errorf("log.level must be one of \"debug\", \"info\", \"warn\", \"error\", got %q", cfg.Log.Level)
	}
	if cfg.Log.Format != LogFormatAuto && cfg.Log.Format != LogFormatJSON && cfg.Log.Format != LogFormatText {
		return fmt.Errorf("log.format must be %q, %q or %q, got %q", LogFormatAuto, LogFormatJSON, LogFormatText, cfg.Log.Format)
	}

	names := make(map[string]bool)
	ports := make(map[int]bool)
	for i, ch := range cfg.Channels {
		if !channelNamePattern.MatchString(ch.Name) {
			return fmt.Errorf("channel name %q must be non-empty and contain only letters, digits, _ or -", ch.Name)
		}
		if names[ch.Name] {
			return fmt.Errorf("duplicate channel name %s", ch.Name)
		}
		names[ch.Name] = true
		if ch.TCPListener.Port == cfg.AdminAPI.Port {
			return fmt.Errorf("channel %s: tcp_listener.port %d collides with admin_api.port", ch.Name, ch.TCPListener.Port)
		}
		if ports[ch.TCPListener.Port] {
			return fmt.Errorf("channel %s: duplicate tcp_listener.port %d", ch.Name, ch.TCPListener.Port)
		}
		ports[ch.TCPListener.Port] = true
		if ch.TCPListener.MaxConnections < 0 {
			return fmt.Errorf("channel %s: tcp_listener.max_connections must be positive, got %d", ch.Name, ch.TCPListener.MaxConnections)
		}
		if ch.TCPListener.MaxConnections == 0 {
			cfg.Channels[i].TCPListener.MaxConnections = DefaultMaxTCPConnections
		}
		if ch.TCPListener.IdleTimeoutSeconds <= 0 {
			return fmt.Errorf("channel %s: tcp_listener.idle_timeout_seconds is required and must be positive, got %d", ch.Name, ch.TCPListener.IdleTimeoutSeconds)
		}
		if err := validateTLS(ch.TCPListener.TLS); err != nil {
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
		if ch.Destination.Auth.TokenEnv == "" {
			return fmt.Errorf("channel %s: destination.auth.token_env is required and must be non-empty", ch.Name)
		}
	}
	return nil
}

func validateTLS(t *TLS) error {
	if t == nil {
		return errors.New("tcp_listener.tls is required")
	}
	if t.Mode != TLSModeOff && t.Mode != TLSModeMTLS {
		return fmt.Errorf("tcp_listener.tls.mode must be %q or %q, got %q", TLSModeOff, TLSModeMTLS, t.Mode)
	}
	if t.Mode == TLSModeOff {
		return nil
	}
	if t.ClientCAPath == "" {
		return errors.New("tcp_listener.tls.client_ca_path is required and must be non-empty")
	}
	if t.ServerCertPath == "" {
		return errors.New("tcp_listener.tls.server_cert_path is required and must be non-empty")
	}
	if t.ServerPKPath == "" {
		return errors.New("tcp_listener.tls.server_pk_path is required and must be non-empty")
	}
	if t.MinVersion == "" {
		return errors.New("tcp_listener.tls.min_version is required")
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

func resolveToken(env string) (string, error) {
	token := os.Getenv(env)
	if token == "" {
		return "", fmt.Errorf("%s must be set to a non-empty bearer token", env)
	}
	return token, nil
}
