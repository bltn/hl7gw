//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

func TestAPIListsChannels(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: channelName, DestURL: sink.URL(), MaxTCPConns: 7, TCPIdleTimeoutSecs: 3600},
		harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct},
	)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	resp, err := gw.APIGet("/channels", "Bearer "+gw.APIToken())
	if err != nil {
		t.Fatalf("get /channels: %v", err)
	}
	if resp.Status != http.StatusOK || resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Fatalf("status = %d, reqSpecErr = %v, respSpecErr = %v; want 200 and spec-clean", resp.Status, resp.ReqSpecErr, resp.RespSpecErr)
	}

	var body apiChannelsPage
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decoding /channels: %v", err)
	}
	if len(body.Channels) != 2 {
		t.Fatalf("channels = %+v, want exactly two", body.Channels)
	}

	plain := body.Channels[0]
	if plain.Name != channelName || plain.TCPListener.MaxConnections != 7 || plain.TCPListener.IdleTimeoutSeconds != 3600 || plain.TCPListener.Port == 0 || plain.Destination.URL != sink.URL() {
		t.Errorf("channel = %+v, want %s with 7 conns, 3600s idle timeout and destination %s", plain, channelName, sink.URL())
	}
	if plain.Destination.Auth.Type != "bearer" || plain.Destination.Auth.TokenEnv != harness.ChannelTokenEnv {
		t.Errorf("auth = %+v, want bearer via %s", plain.Destination.Auth, harness.ChannelTokenEnv)
	}
	if plain.TCPListener.TLS != (apiChannelTLS{Mode: "off"}) {
		t.Errorf("plaintext channel tls = %+v, want mode off with no other fields", plain.TCPListener.TLS)
	}

	secure := body.Channels[1]
	want := apiChannelTLS{
		Mode:           "mtls",
		ClientCAPath:   ct.ClientCAPath,
		ServerCertPath: ct.ServerCertPath,
		ServerPKPath:   ct.ServerPKPath,
		MinVersion:     json.Number("1.2"),
	}
	if secure.Name != secureChannel || secure.TCPListener.TLS != want {
		t.Errorf("mtls channel = %+v, want tls %+v", secure, want)
	}
}
