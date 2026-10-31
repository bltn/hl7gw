package harness

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type HTTPResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (gw *Gateway) HTTPDo(method, path string) (HTTPResponse, error) {
	return request(http.DefaultClient, method, gw.MetricsURL()+path, nil)
}

func (gw *Gateway) SocketDo(method, path string, body []byte) (HTTPResponse, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", gw.socketPath)
		},
	}}
	return request(client, method, "http://hl7gw"+path, body)
}

func request(client *http.Client, method, url string, body []byte) (HTTPResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return HTTPResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{Status: resp.StatusCode, Header: resp.Header, Body: respBody}, nil
}

func (gw *Gateway) WaitForMetricLine(line string, timeout time.Duration) (string, bool) {
	var body string
	found := eventually(timeout, func() bool {
		resp, err := gw.HTTPDo(http.MethodGet, "/metrics")
		if err != nil {
			return false
		}
		body = string(resp.Body)
		return strings.Contains(body, line)
	})
	return body, found
}
