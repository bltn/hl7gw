package api

import (
	"net/http"

	"github.com/bltn/hl7gw/internal/config"
)

type authJSON struct {
	Type      string `json:"type"`
	TokenFile string `json:"token_file"`
}

type destinationJSON struct {
	URL  string   `json:"url"`
	Auth authJSON `json:"auth"`
}

type tlsJSON struct {
	Mode         string `json:"mode"`
	CertFile     string `json:"cert_file,omitempty"`
	KeyFile      string `json:"key_file,omitempty"`
	ClientCAFile string `json:"client_ca_file,omitempty"`
	MinVersion   string `json:"min_version,omitempty"`
}

type sourceJSON struct {
	MaxConnections int             `json:"max_connections"`
	Port           int             `json:"port"`
	IdleTimeout    config.Duration `json:"idle_timeout"`
	TLS            tlsJSON         `json:"tls"`
}

type channelJSON struct {
	Name        string          `json:"name"`
	Source      sourceJSON      `json:"source"`
	Destination destinationJSON `json:"destination"`
}

func (a *api) listChannels(w http.ResponseWriter, _ *http.Request) {
	chs := make([]channelJSON, 0, len(a.channels))
	for _, ch := range a.channels {
		t := tlsJSON{Mode: ch.Source.TLS.Mode}
		if t.Mode == config.TLSModeMTLS {
			t.CertFile = ch.Source.TLS.CertFile
			t.KeyFile = ch.Source.TLS.KeyFile
			t.ClientCAFile = ch.Source.TLS.ClientCAFile
			t.MinVersion = ch.Source.TLS.MinVersion
		}
		chs = append(chs, channelJSON{
			Name: ch.Name,
			Source: sourceJSON{
				MaxConnections: ch.Source.MaxConnections,
				Port:           ch.Source.Port,
				IdleTimeout:    ch.Source.IdleTimeout,
				TLS:            t,
			},
			Destination: destinationJSON{
				URL: ch.Destination.URL.String(),
				Auth: authJSON{
					Type:      ch.Destination.Auth.Type,
					TokenFile: ch.Destination.Auth.TokenFile,
				},
			},
		})
	}
	writeJSON(w, struct {
		Channels []channelJSON `json:"channels"`
	}{Channels: chs})
}
