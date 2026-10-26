package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bltn/hl7gw/internal/console"
	itls "github.com/bltn/hl7gw/internal/tls"
)

const (
	DefaultMaxTCPConnections  = 64
	DefaultDataDir            = "data"
	DefaultPath               = "hl7gw.json"
	DefaultCertReloadInterval = Duration(2 * time.Minute)
	maxPort                   = 65535

	TLSModeOff  = "off"
	TLSModeMTLS = "mtls"

	retentionOff     = "off"
	maxRetentionDays = 36500

	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthBasic  = "basic"

	DefaultLogLevel = "info"
	LogFormatAuto   = "auto"
	LogFormatJSON   = "json"
	LogFormatText   = "text"
)

var errNoDefaultConfig = errors.New("no config file: looked for ./" + DefaultPath + " in the current directory, pass -config <path> to use another")

var (
	LogLevels  = []string{"debug", "info", "warn", "error"}
	LogFormats = []string{LogFormatAuto, LogFormatJSON, LogFormatText}
	AuthTypes  = []string{AuthNone, AuthBearer, AuthBasic}
)

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
	var level slog.Level
	_ = level.UnmarshalText([]byte(l.Level))
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
	Type          string `json:"type"`
	TokenFile     string `json:"token_file,omitempty"`
	Username      string `json:"username,omitempty"`
	PasswordFile  string `json:"password_file,omitempty"`
	authorization string
}

func (a *Auth) Authorization() string {
	return a.authorization
}

func (a *Auth) SecretFile() string {
	switch a.Type {
	case AuthBearer:
		return a.TokenFile
	case AuthBasic:
		return a.PasswordFile
	}
	return ""
}

func (a *Auth) load() error {
	switch a.Type {
	case AuthBearer:
		token, err := readSecret("token", a.TokenFile)
		if err != nil {
			return err
		}
		a.authorization = "Bearer " + token
	case AuthBasic:
		password, err := readSecret("password", a.PasswordFile)
		if err != nil {
			return err
		}
		a.authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(a.Username+":"+password))
	}
	return nil
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
	return []byte(d.String()), nil
}

type Retention struct {
	text string
	days int
}

var DefaultRetention = Retention{text: "30d", days: 30}

func (r *Retention) UnmarshalText(text []byte) error {
	s := string(text)
	if s == retentionOff {
		*r = Retention{text: s}
		return nil
	}
	n, ok := strings.CutSuffix(s, "d")
	days, err := strconv.Atoi(n)
	if !ok || err != nil || days < 1 || days > maxRetentionDays {
		return fmt.Errorf("retention must be %q or a whole number of days such as \"30d\", got %q", retentionOff, s)
	}
	*r = Retention{text: s, days: days}
	return nil
}

func (r Retention) MarshalText() ([]byte, error) {
	return []byte(r.text), nil
}

func (r Retention) Days() int {
	return r.days
}

type Config struct {
	Path               string    `json:"-"`
	Admin              Admin     `json:"admin"`
	DataDir            string    `json:"data_dir"`
	CertReloadInterval Duration  `json:"cert_reload_interval"`
	Retention          Retention `json:"retention"`
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

func ValidatePort(n int) error {
	if n < 1 || n > maxPort {
		return fmt.Errorf("must be a port between 1 and %d, got %d", maxPort, n)
	}
	return nil
}

func ValidateUsername(s string) error {
	if s == "" {
		return errors.New("must not be empty")
	}
	if strings.Contains(s, ":") {
		return fmt.Errorf("must not contain \":\", got %q", s)
	}
	return nil
}

func oneOf(key string, options []string, got string) error {
	if slices.Contains(options, got) {
		return nil
	}
	quoted := make([]string, len(options))
	for i, o := range options {
		quoted[i] = strconv.Quote(o)
	}
	return fmt.Errorf("%s must be %s or %s, got %q", key, strings.Join(quoted[:len(quoted)-1], ", "), quoted[len(quoted)-1], got)
}

func Load(path string) (Config, error) {
	path, err := resolvePath(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := loadAndDecode(path)
	if err != nil {
		return Config{}, err
	}
	cfg.Path = path
	applyDefaults(&cfg)

	err = validate(cfg)
	if err != nil {
		return Config{}, err
	}

	adminToken, err := readSecret("token", cfg.Admin.TokenFile)
	if err != nil {
		return Config{}, fmt.Errorf("admin: %w", err)
	}
	cfg.Admin.token = adminToken

	for _, ch := range cfg.Channels {
		if err := ch.Source.TLS.load(); err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		if err := ch.Destination.Auth.load(); err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
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
	if cfg.Retention == (Retention{}) {
		cfg.Retention = DefaultRetention
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
	if err := ValidatePort(cfg.Admin.Port); err != nil {
		return fmt.Errorf("admin.port %w", err)
	}
	if cfg.Admin.TokenFile == "" {
		return errors.New("admin.token_file is required and must be non-empty")
	}
	if cfg.CertReloadInterval < 0 {
		return fmt.Errorf("cert_reload_interval must be positive, got %s", cfg.CertReloadInterval)
	}
	if err := oneOf("log.level", LogLevels, cfg.Log.Level); err != nil {
		return err
	}
	if err := oneOf("log.format", LogFormats, cfg.Log.Format); err != nil {
		return err
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
		if err := ValidatePort(ch.Source.Port); err != nil {
			return fmt.Errorf("channel %s: source.port %w", ch.Name, err)
		}
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
		if err := validateAuth(ch.Destination.Auth); err != nil {
			return fmt.Errorf("channel %s: %w", ch.Name, err)
		}
	}
	return nil
}

func validateAuth(a *Auth) error {
	if a == nil {
		return errors.New("destination.auth is required")
	}
	if err := oneOf("destination.auth.type", AuthTypes, a.Type); err != nil {
		return err
	}
	fields := []struct{ key, value, owner string }{
		{"token_file", a.TokenFile, AuthBearer},
		{"username", a.Username, AuthBasic},
		{"password_file", a.PasswordFile, AuthBasic},
	}
	for _, f := range fields {
		if a.Type == f.owner && f.value == "" {
			return fmt.Errorf("destination.auth.%s is required and must be non-empty", f.key)
		}
		if a.Type != f.owner && f.value != "" {
			return fmt.Errorf("destination.auth.%s is only valid when type is %q", f.key, f.owner)
		}
	}
	if a.Type == AuthBasic {
		if err := ValidateUsername(a.Username); err != nil {
			return fmt.Errorf("destination.auth.username %w", err)
		}
	}
	return nil
}

func validateTLS(t *TLS) error {
	if t == nil {
		return errors.New("source.tls is required")
	}
	if err := oneOf("source.tls.mode", []string{TLSModeOff, TLSModeMTLS}, t.Mode); err != nil {
		return err
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

func resolvePath(path string) (string, error) {
	if path == "" {
		path = DefaultPath
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return "", errNoDefaultConfig
		}
	}
	return filepath.Abs(path)
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

func readSecret(kind, path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304 -- path is an operator-supplied config value, no traversal boundary
	if err != nil {
		return "", fmt.Errorf("reading %s file: %w", kind, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("%s file %s is empty", kind, path)
	}
	return secret, nil
}
