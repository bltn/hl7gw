//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
		harness.Channel{Name: channelName, DestURL: sink.URL(), MaxTCPConns: 7, IdleTimeout: time.Hour},
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
	if plain.Name != channelName || plain.Source.MaxConnections != 7 || plain.Source.IdleTimeout != "1h" || plain.Source.Port == 0 || plain.Destination.URL != sink.URL() {
		t.Errorf("channel = %+v, want %s with 7 conns, 1h idle timeout and destination %s", plain, channelName, sink.URL())
	}
	if plain.Destination.Auth.Type != "bearer" || plain.Destination.Auth.TokenFile != gw.ChannelTokenFile() {
		t.Errorf("auth = %+v, want bearer from %s", plain.Destination.Auth, gw.ChannelTokenFile())
	}
	if plain.Source.TLS != (apiChannelTLS{Mode: "off"}) {
		t.Errorf("plaintext channel tls = %+v, want mode off with no other fields", plain.Source.TLS)
	}

	secure := body.Channels[1]
	want := apiChannelTLS{
		Mode:         "mtls",
		CertFile:     ct.CertFile,
		KeyFile:      ct.KeyFile,
		ClientCAFile: ct.ClientCAFile,
		MinVersion:   "1.2",
	}
	if secure.Name != secureChannel || secure.Source.TLS != want {
		t.Errorf("mtls channel = %+v, want tls %+v", secure, want)
	}
}
