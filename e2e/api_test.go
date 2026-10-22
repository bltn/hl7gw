//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

const (
	alphaChannel = "alpha"
	betaChannel  = "beta"
	addrInUse    = "address already in use"
	wrongBearer  = "Bearer wrong"
	healthzPath  = "/healthz"
	readyzPath   = "/readyz"
	metricsPath  = "/metrics"
)

type apiAttempt struct {
	AttemptedAt        string  `json:"attempted_at"`
	Outcome            string  `json:"outcome"`
	ResponseStatusCode *int64  `json:"response_status_code"`
	ResponseBody       *string `json:"response_body"`
	Payload            string  `json:"payload"`
	Origin             string  `json:"origin"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

type apiMessage struct {
	ID               int64        `json:"id"`
	CreatedAt        string       `json:"created_at"`
	UpdatedAt        string       `json:"updated_at"`
	ReceivedAt       string       `json:"received_at"`
	RawMessage       string       `json:"raw_message"`
	TransformedXML   *string      `json:"transformed_xml"`
	TransformError   *string      `json:"transform_error"`
	AckMessage       *string      `json:"ack_message"`
	AckOutcome       *string      `json:"ack_outcome"`
	DeliveryAttempts []apiAttempt `json:"delivery_attempts"`
}

type apiMessagesPage struct {
	Data       []apiMessage `json:"data"`
	Pagination struct {
		PageNumber int `json:"pageNumber"`
		PageSize   int `json:"pageSize"`
		Total      int `json:"total"`
	} `json:"pagination"`
	Links struct {
		Self string `json:"self"`
		Next string `json:"next"`
		Prev string `json:"prev"`
	} `json:"links"`
}

type apiAuth struct {
	Type         string `json:"type"`
	TokenFile    string `json:"token_file"`
	Username     string `json:"username"`
	PasswordFile string `json:"password_file"`
}

type destination struct {
	URL  string  `json:"url"`
	Auth apiAuth `json:"auth"`
}

type apiChannelTLS struct {
	Mode         string `json:"mode"`
	CertFile     string `json:"cert_file"`
	KeyFile      string `json:"key_file"`
	ClientCAFile string `json:"client_ca_file"`
	MinVersion   string `json:"min_version"`
}

type apiSource struct {
	MaxConnections int           `json:"max_connections"`
	Port           int           `json:"port"`
	IdleTimeout    string        `json:"idle_timeout"`
	TLS            apiChannelTLS `json:"tls"`
}

type apiChannel struct {
	Name        string      `json:"name"`
	Source      apiSource   `json:"source"`
	Destination destination `json:"destination"`
}

type apiChannelsPage struct {
	Channels []apiChannel `json:"channels"`
}

type apiEndpoint struct {
	name, method, specPath, requestPath, body string
	public                                    bool
}

func apiEndpoints() []apiEndpoint {
	return []apiEndpoint{
		{name: "healthz", method: http.MethodGet, specPath: healthzPath, requestPath: healthzPath, public: true},
		{name: "readyz", method: http.MethodGet, specPath: readyzPath, requestPath: readyzPath, public: true},
		{name: "metrics", method: http.MethodGet, specPath: metricsPath, requestPath: metricsPath, public: true},
		{name: "list messages", method: http.MethodGet, specPath: "/messages", requestPath: "/messages?channel=" + channelName},
		{name: "get message", method: http.MethodGet, specPath: "/messages/{id}", requestPath: "/messages/1?channel=" + channelName},
		{name: "list channels", method: http.MethodGet, specPath: "/channels", requestPath: "/channels"},
		{name: "replay", method: http.MethodPost, specPath: "/replay", requestPath: "/replay", body: fmt.Sprintf(replayBody, channelName, 1)},
		{name: "process message", method: http.MethodPost, specPath: "/process-message", requestPath: processMessagePath, body: fmt.Sprintf(processMessageBody, channelName, adtA01Message)},
	}
}

func callEndpoint(gw *harness.Gateway, ep apiEndpoint, authorization string) (harness.APIResponse, error) {
	if ep.method == http.MethodPost {
		return gw.APIPost(ep.requestPath, authorization, []byte(ep.body))
	}
	return gw.APIGet(ep.requestPath, authorization)
}

func TestAPIEndpointTableMatchesSpec(t *testing.T) {
	t.Parallel()
	ops, err := harness.SpecOperations()
	if err != nil {
		t.Fatalf("loading spec operations: %v", err)
	}

	spec := make(map[string]bool)
	for _, op := range ops {
		spec[op.Method+" "+op.Path] = true
	}
	table := make(map[string]bool)
	for _, ep := range apiEndpoints() {
		table[ep.method+" "+ep.specPath] = true
	}

	for key := range spec {
		if !table[key] {
			t.Errorf("spec operation %s has no row in apiEndpoints", key)
		}
	}
	for key := range table {
		if !spec[key] {
			t.Errorf("apiEndpoints row %s is not an operation in openapi.yaml", key)
		}
	}
}

func TestAPIRejectsBadAuth(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	cases := []struct {
		name          string
		authorization string
	}{
		{name: "no header", authorization: ""},
		{name: "not bearer", authorization: "Basic abc"},
		{name: "wrong token", authorization: wrongBearer},
	}
	for _, ep := range apiEndpoints() {
		if ep.public {
			continue
		}
		for _, c := range cases {
			t.Run(ep.name+" "+c.name, func(t *testing.T) {
				resp, err := callEndpoint(gw, ep, c.authorization)
				if err != nil {
					t.Fatalf("%s %s: %v", ep.method, ep.requestPath, err)
				}
				if resp.Status != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401; body %s", resp.Status, resp.Body)
				}
				if resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
					t.Errorf("reqSpecErr = %v, respSpecErr = %v; want spec-clean", resp.ReqSpecErr, resp.RespSpecErr)
				}
			})
		}
	}

	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestAPIRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for _, ep := range apiEndpoints() {
		if ep.method != http.MethodPost {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			resp, err := gw.APIPost(ep.requestPath, "Bearer "+gw.APIToken(), []byte("{"))
			if err != nil {
				t.Fatalf("post %s: %v", ep.requestPath, err)
			}
			if resp.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", resp.Status, resp.Body)
			}
			if resp.RespSpecErr != nil {
				t.Errorf("response violates openapi.yaml: %v", resp.RespSpecErr)
			}
			if resp.ReqSpecErr == nil {
				t.Errorf("request spec validation error = nil, want error")
			}
		})
	}

	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestAPIRejectsWrongMethods(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	for _, ep := range apiEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			method, wantAllow := http.MethodPost, "GET, HEAD"
			if ep.method == http.MethodPost {
				method, wantAllow = http.MethodGet, "POST"
			}
			resp, err := gw.APIRaw(method, ep.requestPath, "Bearer "+gw.APIToken(), nil)
			if err != nil {
				t.Fatalf("%s %s: %v", method, ep.requestPath, err)
			}
			if resp.Status != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405; body %s", resp.Status, resp.Body)
			}
			if allow := resp.Header.Get("Allow"); allow != wantAllow {
				t.Errorf("Allow = %q, want %q", allow, wantAllow)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
		})
	}

	if rcvd := sink.Received(); len(rcvd) != 0 {
		t.Errorf("sink received %d requests, want none", len(rcvd))
	}
}

func TestAPIRejectsUnknownEndpoint(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	resp, err := gw.APIRaw(http.MethodGet, "/nope", "Bearer "+gw.APIToken(), nil)
	if err != nil {
		t.Fatalf("get /nope: %v", err)
	}
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body %s", resp.Status, resp.Body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

func TestAPIRejectsBadPaginationParams(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	tests := []struct {
		name  string
		query string
	}{
		{name: "pageNumber zero", query: "pageNumber=0"},
		{name: "pageNumber not a number", query: "pageNumber=abc"},
		{name: "pageNumber over max", query: "pageNumber=1000000001"},
		{name: "pageSize zero", query: "pageSize=0"},
		{name: "pageSize not a number", query: "pageSize=abc"},
		{name: "pageSize over max", query: "pageSize=501"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := "/messages?channel=" + channelName + "&" + tc.query
			resp, err := gw.APIGet(path, "Bearer "+gw.APIToken())
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			if resp.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", resp.Status, resp.Body)
			}
			if resp.RespSpecErr != nil {
				t.Errorf("response violates openapi.yaml: %v", resp.RespSpecErr)
			}
			if resp.ReqSpecErr == nil {
				t.Errorf("request spec validation error = nil, want error")
			}
		})
	}
}

func TestAPIServesSpecAndDocs(t *testing.T) {
	t.Parallel()
	sink := harness.NewSink(t, http.StatusOK, "")
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	tests := []struct {
		path        string
		contentType string
		marker      string
	}{
		{path: "/openapi.yaml", contentType: "application/yaml", marker: "openapi:"},
		{path: "/docs", contentType: "text/html; charset=utf-8", marker: "redoc"},
		{path: "/docs/redoc.standalone.js", contentType: "text/javascript; charset=utf-8", marker: "Redoc"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := gw.APIRaw(http.MethodGet, tc.path, "", nil)
			if err != nil {
				t.Fatalf("get %s: %v", tc.path, err)
			}
			if resp.Status != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.Status)
			}
			if ct := resp.Header.Get("Content-Type"); ct != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tc.contentType)
			}
			if !strings.Contains(string(resp.Body), tc.marker) {
				t.Errorf("body does not contain %q", tc.marker)
			}
		})
	}
}

func TestExitsWhenAPIPortUnavailable(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	blocker, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatalf("bind blocker port: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	cfg := fmt.Sprintf(`{"data_dir": %q, "admin": {"port": %d, "token_file": "admin.token"}, "channels": [
		{"name": "solo", `+sourceOK+`, `+destinationOK+`}
	]}`, t.TempDir(), blockedPort)
	expectStartupFailure(t, cfg, errorPrefix, "binding admin api listener", addrInUse)
}

func TestUnwindsAPIListenerOnChannelPortCollision(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	blocker, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatalf("bind blocker port: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	ports, err := harness.FreePorts(1)
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	apiPort := ports[0]

	cfg := fmt.Sprintf(`{"data_dir": %q, "admin": {"port": %d, "token_file": "admin.token"}, "channels": [
		{"name": "solo", "source": {"port": %d, "idle_timeout": "24h", `+tlsOffOK+`}, `+destinationOK+`}
	]}`, t.TempDir(), apiPort, blockedPort)
	expectStartupFailure(t, cfg, errorPrefix, "channel solo", addrInUse)

	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", apiPort))
	if err == nil {
		_ = conn.Close()
		t.Errorf("admin port %d still accepts connections after the gateway exited", apiPort)
	}
}

func pollForAckedMessage(t *testing.T, gw *harness.Gateway) (apiMessagesPage, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page := getMessagesPage(t, gw, "/messages?channel="+channelName)
		if len(page.Data) == 1 && page.Data[0].AckMessage != nil {
			return page, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return apiMessagesPage{}, false
}

func getMessagesPage(t *testing.T, gw *harness.Gateway, pathAndQuery string) apiMessagesPage {
	t.Helper()
	resp, err := gw.APIGet(pathAndQuery, "Bearer "+gw.APIToken())
	if err != nil {
		t.Fatalf("get %s: %v", pathAndQuery, err)
	}
	if resp.Status != http.StatusOK || resp.ReqSpecErr != nil || resp.RespSpecErr != nil {
		t.Fatalf("get %s: status = %d, reqSpecErr = %v, respSpecErr = %v; want 200 and spec-clean; body %s",
			pathAndQuery, resp.Status, resp.ReqSpecErr, resp.RespSpecErr, resp.Body)
	}
	var page apiMessagesPage
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("decoding %s: %v", pathAndQuery, err)
	}
	return page
}
