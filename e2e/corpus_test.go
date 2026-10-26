//go:build e2e

package e2e

import (
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bltn/hl7gw/e2e/internal/harness"
)

//go:embed testdata/corpus
var corpus embed.FS

type corpusMessage struct {
	Name      string
	Wire      string
	ControlID string
}

func TestCorpusSmoke(t *testing.T) {
	t.Parallel()
	msgs, err := loadCorpus()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("corpus is empty")
	}
	t.Logf("%d messages in the corpus", len(msgs))

	sink := harness.NewSink(t, http.StatusOK, sinkBody)
	gw, err := harness.StartGateway(t, harness.Channel{Name: channelName, DestURL: sink.URL()})
	if err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	conn, err := gw.Dial(channelName)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	inbound := conn.ReadMessages(len(msgs), 2*time.Minute)
	for _, m := range msgs {
		if err := conn.Send(m.Wire); err != nil {
			t.Fatalf("send %s: %v", m.Name, err)
		}
	}

	acks := <-inbound
	if acks.Err != nil {
		t.Fatalf("read ACKs: %v", acks.Err)
	}
	for i, ack := range acks.Messages {
		m := msgs[i]
		code, _ := harness.Field(ack, "MSA", 1)
		echoed, _ := harness.Field(ack, "MSA", 2)
		if code != "AA" || echoed != m.ControlID {
			t.Errorf("%s: MSA|%s|%s, want MSA|AA|%s", m.Name, code, echoed, m.ControlID)
		}
	}
	if conn.HasInboundWithin(quietWindow) {
		t.Error("gateway sent more ACKs than messages")
	}

	rows, attempts := stopAndReadAll(t, gw)
	if len(rows) != len(msgs) {
		t.Fatalf("got %d message rows, want %d", len(rows), len(msgs))
	}
	byMessage := map[int64][]harness.DeliveryAttemptRow{}
	for _, a := range attempts {
		byMessage[a.MessageID] = append(byMessage[a.MessageID], a)
	}
	for i, row := range rows {
		m, as := msgs[i], byMessage[row.ID]
		if row.Raw != m.Wire {
			t.Errorf("%s: persisted raw message differs from what was sent", m.Name)
		}
		if row.XML == nil {
			t.Errorf("%s: no transform persisted", m.Name)
		} else if err := wellFormedXML(*row.XML); err != nil {
			t.Errorf("%s: transform is not well-formed XML: %v", m.Name, err)
		}
		if len(as) != 1 || as[0].Outcome != outcomeSuccess || as[0].Origin != originInbound {
			t.Errorf("%s: attempts = %+v, want one successful inbound attempt", m.Name, as)
		}
	}
	if got := len(sink.Received()); got != len(msgs) {
		t.Errorf("sink received %d messages, want %d", got, len(msgs))
	}
}

func loadCorpus() ([]corpusMessage, error) {
	var out []corpusMessage
	err := fs.WalkDir(corpus, "testdata/corpus", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(path, "/messages/") {
			return err
		}
		data, err := corpus.ReadFile(path)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, "testdata/corpus/")
		for i, wire := range splitCorpusFile(string(data)) {
			name := fmt.Sprintf("%s#%d", rel, i)
			if strings.ContainsAny(wire, "\x0b\x1c") {
				return fmt.Errorf("%s contains MLLP framing bytes", name)
			}
			cid, _ := harness.Field(wire, "MSH", 10)
			out = append(out, corpusMessage{Name: name, Wire: wire, ControlID: cid})
		}
		return nil
	})
	return out, err
}

func splitCorpusFile(data string) []string {
	data = strings.TrimPrefix(data, "\ufeff")
	data = strings.ReplaceAll(data, "\r\n", "\r")
	data = strings.ReplaceAll(data, "\n", "\r")

	var msgs []string
	segments := make([]string, 0, strings.Count(data, "\r")+1)
	flush := func() {
		if len(segments) > 0 {
			msgs = append(msgs, strings.Join(segments, "\r"))
			segments = segments[:0]
		}
	}
	for _, line := range strings.Split(data, "\r") {
		id := line[:min(3, len(line))]
		switch id {
		case "", "FHS", "BHS", "BTS", "FTS":
			continue
		case "MSH":
			flush()
		}
		segments = append(segments, line)
	}
	flush()
	return msgs
}

func wellFormedXML(doc string) error {
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		if _, err := dec.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
