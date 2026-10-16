package api

import (
	"encoding/json"
	"net/http"

	"github.com/bltn/hl7gw/internal/config"
)

type authJSON struct {
	Type     string `json:"type"`
	TokenEnv string `json:"token_env"`
}

type destinationJSON struct {
	URL  string   `json:"url"`
	Auth authJSON `json:"auth"`
}

type tlsJSON struct {
	Mode           string      `json:"mode"`
	ClientCAPath   string      `json:"client_ca_path,omitempty"`
	ServerCertPath string      `json:"server_cert_path,omitempty"`
	ServerPKPath   string      `json:"server_pk_path,omitempty"`
	MinVersion     json.Number `json:"min_version,omitempty"`
}

type tcpListenerJSON struct {
	MaxConnections     int     `json:"max_connections"`
	Port               int     `json:"port"`
	IdleTimeoutSeconds int     `json:"idle_timeout_seconds"`
	TLS                tlsJSON `json:"tls"`
}

type channelJSON struct {
	Name        string          `json:"name"`
	TCPListener tcpListenerJSON `json:"tcp_listener"`
	Destination destinationJSON `json:"destination"`
}

func (a *api) listChannels(w http.ResponseWriter, _ *http.Request) {
	chs := make([]channelJSON, 0, len(a.channels))
	for _, ch := range a.channels {
		t := tlsJSON{Mode: ch.TCPListener.TLS.Mode}
		if t.Mode == config.TLSModeMTLS {
			t.ClientCAPath = ch.TCPListener.TLS.ClientCAPath
			t.ServerCertPath = ch.TCPListener.TLS.ServerCertPath
			t.ServerPKPath = ch.TCPListener.TLS.ServerPKPath
			t.MinVersion = ch.TCPListener.TLS.MinVersion
		}
		chs = append(chs, channelJSON{
			Name: ch.Name,
			TCPListener: tcpListenerJSON{
				MaxConnections:     ch.TCPListener.MaxConnections,
				Port:               ch.TCPListener.Port,
				IdleTimeoutSeconds: ch.TCPListener.IdleTimeoutSeconds,
				TLS:                t,
			},
			Destination: destinationJSON{
				URL: ch.Destination.URL.String(),
				Auth: authJSON{
					Type:     ch.Destination.Auth.Type,
					TokenEnv: ch.Destination.Auth.TokenEnv,
				},
			},
		})
	}
	writeJSON(w, struct {
		Channels []channelJSON `json:"channels"`
	}{Channels: chs})
}
