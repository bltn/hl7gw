package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/console"
)

type UsageError struct {
	Msg string
}

func (e *UsageError) Error() string { return e.Msg }

func Messages(ctx context.Context, args []string, stdout, stderr io.Writer, term console.Terminal) error {
	fs, c := flags("messages", stdout, stderr, term)
	limit := fs.Int("limit", defaultLimit, "how many messages to list, newest first")
	before := fs.Int64("before", 0, "list only messages with an id below this one")
	_ = fs.Parse(args)
	channel, rawID, err := positionals(fs, "<channel> [<id>]", 1)
	if err != nil {
		return err
	}
	if *limit < 1 {
		return &UsageError{Msg: fmt.Sprintf("-limit must be at least 1, got %d", *limit)}
	}
	if *limit > maxLimit {
		return &UsageError{Msg: fmt.Sprintf("-limit must be at most %d, got %d", maxLimit, *limit)}
	}
	if *before < 0 {
		return &UsageError{Msg: fmt.Sprintf("-before must be a message id, got %d", *before)}
	}
	q := url.Values{paramChannel: {channel}}
	if rawID == "" {
		q.Set("limit", strconv.Itoa(*limit))
		q.Set("before", strconv.FormatInt(*before, 10))
		return c.call(ctx, http.MethodGet, "/messages", q, nil, c.out.summaries)
	}
	id, ok := parseID(rawID)
	if !ok {
		return &UsageError{Msg: fmt.Sprintf("message id must be a positive integer, got %q", rawID)}
	}
	return c.call(ctx, http.MethodGet, "/messages/"+strconv.FormatInt(id, 10), q, nil, c.out.message)
}

func Replay(ctx context.Context, args []string, stdout, stderr io.Writer, term console.Terminal) error {
	fs, c := flags("replay", stdout, stderr, term)
	_ = fs.Parse(args)
	channel, rawID, err := positionals(fs, "<channel> <id>", 2)
	if err != nil {
		return err
	}
	id, ok := parseID(rawID)
	if !ok {
		return &UsageError{Msg: fmt.Sprintf("message id must be a positive integer, got %q", rawID)}
	}
	q := url.Values{paramChannel: {channel}, "id": {strconv.FormatInt(id, 10)}}
	return c.call(ctx, http.MethodPost, "/replay", q, nil, c.out.attempt)
}

func Process(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, term console.Terminal) error {
	fs, c := flags("process", stdout, stderr, term)
	dryRun := fs.Bool("dry-run", false, "transform only; persist and deliver nothing")
	_ = fs.Parse(args)
	channel, file, err := positionals(fs, "[-dry-run] <channel> [<file>]", 1)
	if err != nil {
		return err
	}
	raw, err := readMessage(stdin, file)
	if err != nil {
		return err
	}
	if raw == "" {
		return &UsageError{Msg: "process: the message is empty"}
	}
	q := url.Values{paramChannel: {channel}}
	emit := c.out.message
	if *dryRun {
		q.Set("dry_run", "1")
		emit = c.out.dryRun
	}
	return c.call(ctx, http.MethodPost, "/process", q, strings.NewReader(raw), emit)
}

type client struct {
	configPath string
	out        output
}

func flags(name string, stdout, stderr io.Writer, term console.Terminal) (*flag.FlagSet, *client) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.SetOutput(stderr)
	c := &client{out: output{w: stdout, term: term}}
	fs.StringVar(&c.configPath, "config", "", "path to config file (default ./"+config.DefaultPath+")")
	fs.BoolVar(&c.out.pretty, "pretty", false, "print a readable view instead of JSON")
	return fs, c
}

func positionals(fs *flag.FlagSet, synopsis string, required int) (string, string, error) {
	pos := fs.Args()
	if len(pos) < required || len(pos) > 2 {
		return "", "", &UsageError{Msg: fs.Name() + " takes " + synopsis}
	}
	if len(pos) == 1 {
		return pos[0], "", nil
	}
	return pos[0], pos[1], nil
}

func parseID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id >= 1
}

func readMessage(stdin io.Reader, file string) (string, error) {
	in := stdin
	if file != "" {
		f, err := os.Open(file) //nolint:gosec // G304 -- path is an operator-supplied argument, no traversal boundary
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("reading the message: %w", err)
	}
	return string(data), nil
}

func (c *client) call(ctx context.Context, method, path string, q url.Values, body io.Reader, emit func(io.Reader) error) error {
	cfg, err := config.Read(c.configPath)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", cfg.Socket)
		},
	}}
	req, _ := http.NewRequestWithContext(ctx, method, "http://hl7gw"+path+"?"+q.Encode(), body)
	resp, err := httpClient.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("cannot reach the gateway at %s: %w", cfg.Socket, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return errors.New(strings.TrimSpace(string(msg)))
	}
	return emit(resp.Body)
}
