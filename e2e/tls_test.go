//go:build e2e

package e2e

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	secureChannel = "secure"

	twoReloadTicks = 2*harness.TLSReloadInterval + harness.TLSReloadInterval/2
)

func TestExitsOnInvalidChannelTLS(t *testing.T) {
	t.Parallel()
	ctA, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	ctB, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate second mtls material: %v", err)
	}

	dir := t.TempDir()
	writeFile := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	emptyCA := writeFile("empty-ca.pem", nil)
	garbageCA := writeFile("garbage-ca.pem", []byte("this is not a pem file"))
	trailingCA := writeFile("trailing-ca.pem", append(append([]byte{}, ctA.CA.CertPEM...), "trailing garbage"...))
	bogusDERCA := writeFile("bogus-der-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not real der")}))
	garbageCert := writeFile("garbage-cert.pem", []byte("not a certificate"))

	goodTLS := mtlsJSON(ctA.ClientCAFile, ctA.CertFile, ctA.KeyFile, "1.2")

	tests := []struct {
		name       string
		badTLS     string
		wantStderr []string
	}{
		{
			name:       "missing client_ca_file",
			badTLS:     fmt.Sprintf(`{"mode": "mtls", "cert_file": %q, "key_file": %q, "min_version": "1.2"}`, ctA.CertFile, ctA.KeyFile),
			wantStderr: []string{"channel bad: source.tls.client_ca_file is required and must be non-empty"},
		},
		{
			name:       "missing cert_file",
			badTLS:     fmt.Sprintf(`{"mode": "mtls", "client_ca_file": %q, "key_file": %q, "min_version": "1.2"}`, ctA.ClientCAFile, ctA.KeyFile),
			wantStderr: []string{"channel bad: source.tls.cert_file is required and must be non-empty"},
		},
		{
			name:       "missing key_file",
			badTLS:     fmt.Sprintf(`{"mode": "mtls", "client_ca_file": %q, "cert_file": %q, "min_version": "1.2"}`, ctA.ClientCAFile, ctA.CertFile),
			wantStderr: []string{"channel bad: source.tls.key_file is required and must be non-empty"},
		},
		{
			name:       "missing min_version",
			badTLS:     fmt.Sprintf(`{"mode": "mtls", "client_ca_file": %q, "cert_file": %q, "key_file": %q}`, ctA.ClientCAFile, ctA.CertFile, ctA.KeyFile),
			wantStderr: []string{"channel bad: source.tls.min_version is required"},
		},
		{
			name:       "garbage min_version",
			badTLS:     mtlsJSON(ctA.ClientCAFile, ctA.CertFile, ctA.KeyFile, "potato"),
			wantStderr: []string{`channel bad: invalid tls min_version "potato", valid versions: 1.2, 1.3`},
		},
		{
			name:       "unknown min_version",
			badTLS:     mtlsJSON(ctA.ClientCAFile, ctA.CertFile, ctA.KeyFile, "7"),
			wantStderr: []string{`channel bad: invalid tls min_version "7", valid versions: 1.2, 1.3`},
		},
		{
			name:       "unsupported min_version 1.4",
			badTLS:     mtlsJSON(ctA.ClientCAFile, ctA.CertFile, ctA.KeyFile, "1.4"),
			wantStderr: []string{`channel bad: invalid tls min_version "1.4", valid versions: 1.2, 1.3`},
		},
		{
			name:       "deprecated min_version 1.1",
			badTLS:     mtlsJSON(ctA.ClientCAFile, ctA.CertFile, ctA.KeyFile, "1.1"),
			wantStderr: []string{`channel bad: invalid tls min_version "1.1", valid versions: 1.2, 1.3`},
		},
		{
			name:       "server cert and key mismatch",
			badTLS:     mtlsJSON(ctA.ClientCAFile, ctA.CertFile, ctB.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: loading server certificate and key", "private key does not match public key"},
		},
		{
			name:       "garbage server cert file",
			badTLS:     mtlsJSON(ctA.ClientCAFile, garbageCert, ctA.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: loading server certificate and key"},
		},
		{
			name:       "missing client CA file",
			badTLS:     mtlsJSON(filepath.Join(dir, "does-not-exist.pem"), ctA.CertFile, ctA.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: reading client CA file", noSuchFile},
		},
		{
			name:       "empty client CA file",
			badTLS:     mtlsJSON(emptyCA, ctA.CertFile, ctA.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: client CA file contains no certificates"},
		},
		{
			name:       "garbage client CA file",
			badTLS:     mtlsJSON(garbageCA, ctA.CertFile, ctA.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: client CA file has trailing data after the last PEM block"},
		},
		{
			name:       "client CA file with trailing garbage",
			badTLS:     mtlsJSON(trailingCA, ctA.CertFile, ctA.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: client CA file has trailing data after the last PEM block"},
		},
		{
			name:       "client CA PEM block with bogus DER",
			badTLS:     mtlsJSON(bogusDERCA, ctA.CertFile, ctA.KeyFile, "1.2"),
			wantStderr: []string{"channel bad: parsing CA certificate 1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := `{` + metricsOK + `, "channels": [
				{"name": "good", "source": {"port": 9998, "idle_timeout": "24h", "tls": ` + goodTLS + `}, ` + destinationOK + `},
				{"name": "bad", "source": {"port": 9999, "idle_timeout": "24h", "tls": ` + tc.badTLS + `}, ` + destinationOK + `}
			]}`
			expectStartupFailure(t, body, tc.wantStderr...)
		})
	}
}

func mtlsJSON(caPath, certPath, keyPath, minVersion string) string {
	return fmt.Sprintf(`{"mode": "mtls", "cert_file": %q, "key_file": %q, "client_ca_file": %q, "min_version": %q}`,
		certPath, keyPath, caPath, minVersion)
}

func TestMTLSMessageFlow(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	listeningMTLS := func(r harness.LogRecord) bool {
		return isMsgOn(msgListening, secureChannel)(r) && r.Attr("tls") == "mtls" && r.Attr("serial") != "" && r.Attr("expires") != ""
	}
	if !gw.WaitForLog(5*time.Second, listeningMTLS) {
		t.Errorf("listening record missing the server cert details\n--- stdout ---\n%s", gw.Stdout())
	}

	ack, err := gw.SendAndAck(secureChannel, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack over mtls: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	if !gw.WaitForLog(5*time.Second, withAttr(isMsgOn(msgClientCertAccepted, secureChannel), "cn", "client")) {
		t.Errorf("stdout missing the client cert accepted record for cn=client\n--- stdout ---\n%s", gw.Stdout())
	}
	if !sink.WaitForReceived(1) {
		t.Errorf("sink received %d messages, want 1", len(sink.Received()))
	}
}

func TestMTLSRejectsSenderWithoutClientCert(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	conn, hsErr := gw.DialTLS(secureChannel, nil)
	if hsErr == nil {
		defer func() { _ = conn.Close() }()
		_ = conn.Send(adtA01Message)
		if !conn.ClosedWithin(5 * time.Second) {
			t.Errorf("connection stayed open after a certificate-less handshake")
		}
	}

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgHandshakeFailed, secureChannel)) {
		t.Errorf("stderr missing the handshake failed line")
	}
	if gw.WaitForLog(quietWindow, isMsg(msgMessageProcessed)) {
		t.Errorf("a message was processed despite the rejected handshake")
	}
	if got := sink.Received(); len(got) != 0 {
		t.Errorf("sink received %d messages, want none", len(got))
	}
}

func TestMTLSRejectsClientCertFromWrongCA(t *testing.T) {
	t.Parallel()
	ctA, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	ctB, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate second mtls material: %v", err)
	}
	sinkA := harness.NewSink(t, http.StatusOK, "")
	sinkB := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: alphaChannel, DestURL: sinkA.URL(), TLS: ctA},
		harness.Channel{Name: betaChannel, DestURL: sinkB.URL(), TLS: ctB},
	)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	conn, hsErr := gw.DialTLS(alphaChannel, ctB.ClientLeaf)
	if hsErr == nil {
		defer func() { _ = conn.Close() }()
		_ = conn.Send(adtA01Message)
		if !conn.ClosedWithin(5 * time.Second) {
			t.Errorf("connection stayed open despite a client cert from another channel's CA")
		}
	}

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgHandshakeFailed, "alpha")) {
		t.Errorf("stderr missing the handshake failed line for channel alpha")
	}
	if got := sinkA.Received(); len(got) != 0 {
		t.Errorf("alpha sink received %d messages, want none", len(got))
	}

	ack, err := gw.SendAndAck(betaChannel, adtA01Message)
	if err != nil {
		t.Fatalf("send and ack on beta with its own cert: %v", err)
	}
	if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
		t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !sinkB.WaitForReceived(1) {
		t.Errorf("beta sink received %d messages, want 1", len(sinkB.Received()))
	}
}

func TestMTLSRejectsPlaintextSender(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	conn, err := gw.DialRaw(secureChannel)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send plaintext MLLP frame: %v", err)
	}

	if !gw.WaitForLog(5*time.Second, isMsgOn(msgHandshakeFailed, secureChannel)) {
		t.Errorf("stderr missing the handshake failed line")
	}
	if !conn.ClosedWithin(5 * time.Second) {
		t.Errorf("connection stayed open after plaintext bytes hit an mtls listener")
	}
	if gw.WaitForLog(quietWindow, isMsg(msgMessageProcessed)) {
		t.Errorf("a message was processed despite the plaintext dial")
	}
	if got := sink.Received(); len(got) != 0 {
		t.Errorf("sink received %d messages, want none", len(got))
	}
}

func TestMTLSServesRotatedCert(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t,
		harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct},
		harness.Channel{Name: "plain", DestURL: sink.URL()},
	)
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	oldLeaf := ct.ServerLeaf
	oldConn, err := gw.Dial(secureChannel)
	if err != nil {
		t.Fatalf("dial before rotation: %v", err)
	}
	defer func() { _ = oldConn.Close() }()
	serial, ok := oldConn.PeerSerial()
	if !ok {
		t.Fatal("pre-rotation connection is not TLS")
	}
	if serial.Cmp(oldLeaf.Cert.SerialNumber) != 0 {
		t.Errorf("pre-rotation handshake served serial %s, want %s", serial, oldLeaf.Cert.SerialNumber)
	}

	newLeaf, err := ct.RotateServerLeaf()
	if err != nil {
		t.Fatalf("rotate server leaf: %v", err)
	}
	reloaded := withAttr(withAttr(isMsgOn(msgCertReloaded, secureChannel), "serial", newLeaf.Cert.SerialNumber.String()),
		"expires", newLeaf.Cert.NotAfter.UTC().Format(time.RFC3339))
	if !gw.WaitForLog(10*time.Second, reloaded) {
		t.Fatalf("stdout missing the server cert reloaded record for serial %s\n--- stdout ---\n%s", newLeaf.Cert.SerialNumber, gw.Stdout())
	}

	time.Sleep(twoReloadTicks)
	if got := gw.CountLogs(isMsg(msgCertReloaded)); got != 1 {
		t.Errorf("reloaded line logged %d times, want 1 (unchanged ticks must stay silent)\n--- stdout ---\n%s", got, gw.Stdout())
	}

	newConn, err := gw.Dial(secureChannel)
	if err != nil {
		t.Fatalf("dial after rotation: %v", err)
	}
	defer func() { _ = newConn.Close() }()
	serial, ok = newConn.PeerSerial()
	if !ok {
		t.Fatal("post-rotation connection is not TLS")
	}
	if serial.Cmp(newLeaf.Cert.SerialNumber) != 0 {
		t.Errorf("post-rotation handshake served serial %s, want rotated %s", serial, newLeaf.Cert.SerialNumber)
	}

	for name, conn := range map[string]*harness.Conn{"pre-rotation": oldConn, "post-rotation": newConn} {
		if err := conn.Send(adtA01Message); err != nil {
			t.Fatalf("send on %s connection: %v", name, err)
		}
		ack, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ack on %s connection: %v", name, err)
		}
		if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
			t.Errorf("ACK mismatch on %s connection:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
	if !sink.WaitForReceived(2) {
		t.Errorf("sink received %d messages, want 2", len(sink.Received()))
	}
}

func TestMTLSServesRotatedClientCA(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	oldClient := ct.ClientLeaf
	oldConn, err := gw.Dial(secureChannel)
	if err != nil {
		t.Fatalf("dial before rotation: %v", err)
	}
	defer func() { _ = oldConn.Close() }()

	newClient, err := ct.RotateClientCA()
	if err != nil {
		t.Fatalf("rotate client ca: %v", err)
	}
	if !gw.WaitForLog(10*time.Second, withAttr(isMsgOn(msgCAReloaded, secureChannel), "certs", "1")) {
		t.Fatalf("stdout missing the client ca pool reloaded record\n--- stdout ---\n%s", gw.Stdout())
	}

	time.Sleep(twoReloadTicks)
	if got := gw.CountLogs(isMsg(msgCAReloaded)); got != 1 {
		t.Errorf("reloaded line logged %d times, want 1 (unchanged ticks must stay silent)\n--- stdout ---\n%s", got, gw.Stdout())
	}
	if gw.CountLogs(isMsg(msgCertReloaded)) != 0 {
		t.Errorf("server cert reload logged despite only the ca changing\n--- stdout ---\n%s", gw.Stdout())
	}

	newConn, err := gw.DialTLS(secureChannel, newClient)
	if err != nil {
		t.Fatalf("dial with client cert from rotated ca: %v", err)
	}
	defer func() { _ = newConn.Close() }()
	serial, ok := newConn.PeerSerial()
	if !ok {
		t.Fatal("post-rotation connection is not TLS")
	}
	if serial.Cmp(ct.ServerLeaf.Cert.SerialNumber) != 0 {
		t.Errorf("post-rotation handshake served serial %s, want unchanged %s", serial, ct.ServerLeaf.Cert.SerialNumber)
	}

	rejected, hsErr := gw.DialTLS(secureChannel, oldClient)
	if hsErr == nil {
		defer func() { _ = rejected.Close() }()
		_ = rejected.Send(adtA01Message)
		if !rejected.ClosedWithin(5 * time.Second) {
			t.Errorf("connection stayed open despite a client cert from the replaced CA")
		}
	}
	if !gw.WaitForLog(5*time.Second, isMsgOn(msgHandshakeFailed, secureChannel)) {
		t.Errorf("stderr missing the handshake failed line")
	}

	for name, conn := range map[string]*harness.Conn{"pre-rotation": oldConn, "new-ca": newConn} {
		if err := conn.Send(adtA01Message); err != nil {
			t.Fatalf("send on %s connection: %v", name, err)
		}
		ack, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ack on %s connection: %v", name, err)
		}
		if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
			t.Errorf("ACK mismatch on %s connection:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
	if !sink.WaitForReceived(2) {
		t.Errorf("sink received %d messages, want 2", len(sink.Received()))
	}
}

func TestMTLSKeepsServingWhenReloadFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		corrupt func(ct, other *harness.ChannelTLS) error
	}{
		{
			name: "cert garbage",
			corrupt: func(ct, _ *harness.ChannelTLS) error {
				return os.WriteFile(ct.CertFile, []byte("not a certificate"), 0o600)
			},
		},
		{
			name: "key from different pair",
			corrupt: func(ct, other *harness.ChannelTLS) error {
				return os.WriteFile(ct.KeyFile, other.ServerLeaf.KeyPEM, 0o600)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := harness.NewMTLS(t)
			if err != nil {
				t.Fatalf("generate mtls material: %v", err)
			}
			other, err := harness.NewMTLS(t)
			if err != nil {
				t.Fatalf("generate second mtls material: %v", err)
			}
			sink := harness.NewSink(t, http.StatusOK, "")
			gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
			if err != nil {
				t.Fatalf("start gateway: %v", err)
			}
			defer func() {
				if err := gw.Stop(); err != nil {
					t.Errorf("gateway exited uncleanly: %v", err)
				}
			}()

			if err := tc.corrupt(ct, other); err != nil {
				t.Fatalf("corrupt server pair on disk: %v", err)
			}
			if !gw.WaitForLog(10*time.Second, isMsgOn(msgCertReloadFailed, secureChannel)) {
				t.Fatalf("stdout missing the cert reload failed record\n--- stdout ---\n%s", gw.Stdout())
			}

			conn, err := gw.Dial(secureChannel)
			if err != nil {
				t.Fatalf("dial after failed reload: %v", err)
			}
			defer func() { _ = conn.Close() }()
			serial, ok := conn.PeerSerial()
			if !ok {
				t.Fatal("connection is not TLS")
			}
			if serial.Cmp(ct.ServerLeaf.Cert.SerialNumber) != 0 {
				t.Errorf("handshake after failed reload served serial %s, want previous %s", serial, ct.ServerLeaf.Cert.SerialNumber)
			}
			if err := conn.Send(adtA01Message); err != nil {
				t.Fatalf("send after failed reload: %v", err)
			}
			if _, err := conn.ReadMessage(); err != nil {
				t.Fatalf("read ack after failed reload: %v", err)
			}
		})
	}
}

func TestMTLSKeepsVerifyingWhenCAReloadFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contents []byte
	}{
		{name: "garbage", contents: []byte("not a pem file")},
		{name: "empty", contents: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := harness.NewMTLS(t)
			if err != nil {
				t.Fatalf("generate mtls material: %v", err)
			}
			sink := harness.NewSink(t, http.StatusOK, "")
			gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
			if err != nil {
				t.Fatalf("start gateway: %v", err)
			}
			defer func() {
				if err := gw.Stop(); err != nil {
					t.Errorf("gateway exited uncleanly: %v", err)
				}
			}()

			if err := os.WriteFile(ct.ClientCAFile, tc.contents, 0o600); err != nil {
				t.Fatalf("corrupt client ca on disk: %v", err)
			}
			if !gw.WaitForLog(10*time.Second, isMsgOn(msgCAReloadFailed, secureChannel)) {
				t.Fatalf("stdout missing the ca reload failed record\n--- stdout ---\n%s", gw.Stdout())
			}

			conn, err := gw.Dial(secureChannel)
			if err != nil {
				t.Fatalf("dial after failed ca reload: %v", err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.Send(adtA01Message); err != nil {
				t.Fatalf("send after failed ca reload: %v", err)
			}
			ack, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read ack after failed ca reload: %v", err)
			}
			if got, want := harness.NormalizeACK(ack), harness.NormalizeACK(adtA01AckAA); got != want {
				t.Errorf("ACK mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
			if gw.CountLogs(isMsg(msgCAReloaded)) != 0 {
				t.Errorf("ca reload logged despite the corrupt file\n--- stdout ---\n%s", gw.Stdout())
			}
		})
	}
}

func TestMTLSServerCertRotationSurvivesCAReloadFailure(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	if err := os.WriteFile(ct.ClientCAFile, []byte("not a pem file"), 0o600); err != nil {
		t.Fatalf("corrupt client ca on disk: %v", err)
	}
	newLeaf, err := ct.RotateServerLeaf()
	if err != nil {
		t.Fatalf("rotate server leaf: %v", err)
	}

	reloaded := withAttr(withAttr(isMsgOn(msgCertReloaded, secureChannel), "serial", newLeaf.Cert.SerialNumber.String()),
		"expires", newLeaf.Cert.NotAfter.UTC().Format(time.RFC3339))
	if !gw.WaitForLog(10*time.Second, reloaded) {
		t.Fatalf("stdout missing the server cert reloaded record for serial %s\n--- stdout ---\n%s", newLeaf.Cert.SerialNumber, gw.Stdout())
	}
	if !gw.WaitForLog(10*time.Second, isMsgOn(msgCAReloadFailed, secureChannel)) {
		t.Fatalf("stdout missing the ca reload failed record\n--- stdout ---\n%s", gw.Stdout())
	}
	if gw.CountLogs(isMsg(msgCAReloaded)) != 0 {
		t.Errorf("ca reload logged despite the corrupt file\n--- stdout ---\n%s", gw.Stdout())
	}

	conn, err := gw.Dial(secureChannel)
	if err != nil {
		t.Fatalf("dial after rotation: %v", err)
	}
	defer func() { _ = conn.Close() }()
	serial, ok := conn.PeerSerial()
	if !ok {
		t.Fatal("connection is not TLS")
	}
	if serial.Cmp(newLeaf.Cert.SerialNumber) != 0 {
		t.Errorf("handshake served serial %s, want rotated %s", serial, newLeaf.Cert.SerialNumber)
	}
	if err := conn.Send(adtA01Message); err != nil {
		t.Fatalf("send after rotation: %v", err)
	}
	if _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read ack after rotation: %v", err)
	}
}

func TestMTLSReloadSilentWhenUnchanged(t *testing.T) {
	t.Parallel()
	ct, err := harness.NewMTLS(t)
	if err != nil {
		t.Fatalf("generate mtls material: %v", err)
	}
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: secureChannel, DestURL: sink.URL(), TLS: ct})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	defer func() {
		if err := gw.Stop(); err != nil {
			t.Errorf("gateway exited uncleanly: %v", err)
		}
	}()

	assertSilent := func(stage string) {
		t.Helper()
		for _, msg := range []string{msgCertReloaded, msgCAReloaded, msgCertReloadFailed, msgCAReloadFailed} {
			if gw.CountLogs(isMsg(msg)) != 0 {
				t.Errorf("%q logged despite %s\n--- stdout ---\n%s", msg, stage, gw.Stdout())
			}
		}
	}

	time.Sleep(twoReloadTicks)
	assertSilent("unchanged files")

	if err := harness.WriteFileAtomic(ct.ClientCAFile, ct.CA.CertPEM); err != nil {
		t.Fatalf("rewrite client ca: %v", err)
	}
	if err := harness.WriteFileAtomic(ct.CertFile, ct.ServerLeaf.CertPEM); err != nil {
		t.Fatalf("rewrite server cert: %v", err)
	}

	time.Sleep(twoReloadTicks)
	assertSilent("byte-identical rewrites")
}
