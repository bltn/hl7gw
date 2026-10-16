package delivery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	backoff     = 500 * time.Millisecond
	httpTimeout = 10 * time.Second
)

type client struct {
	client        *http.Client
	url           *url.URL
	authorization string
	stopping      <-chan struct{}
}

type attempt struct {
	at         time.Time
	elapsed    time.Duration
	statusCode int
	body       string
	err        error
}

func newClient(u *url.URL, token string, stopping <-chan struct{}) *client {
	return &client{client: &http.Client{Timeout: httpTimeout}, url: u, authorization: "Bearer " + token, stopping: stopping}
}

func (c *client) postHL7Message(ctx context.Context, message string) ([]attempt, error) {
	first := c.post(ctx, message)
	if first.err == nil {
		return []attempt{first}, nil
	}

	select {
	case <-time.After(backoff):
	case <-c.stopping:
		return []attempt{first}, first.err
	}

	second := c.post(ctx, message)
	return []attempt{first, second}, second.err
}

func (c *client) post(ctx context.Context, message string) attempt {
	a := c.do(ctx, message)
	a.elapsed = time.Since(a.at)
	return a
}

func (c *client) do(ctx context.Context, message string) attempt {
	a := attempt{at: time.Now()}

	req := &http.Request{
		Method: "POST",
		URL:    c.url,
		Header: http.Header{
			"Authorization": {c.authorization},
			"Content-Type":  {"application/xml"},
		},
		Body:          io.NopCloser(strings.NewReader(message)),
		ContentLength: int64(len(message)),
	}

	resp, err := c.client.Do(req.WithContext(ctx))
	if err != nil {
		a.err = err
		return a
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	a.statusCode = resp.StatusCode
	a.body = string(body)

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		a.err = fmt.Errorf("non-successful status code: %d", resp.StatusCode)
	}

	return a
}
