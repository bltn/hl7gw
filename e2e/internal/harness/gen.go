package harness

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"strings"
)

type Generated struct {
	Raw       string
	ControlID string
}

type MessageGen struct {
	rng *rand.Rand
	n   int
}

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

var (
	genMessageTypes = []string{"ADT^A01", "ADT^A08", "ORU^R01", "ORM^O01"}
	genSegmentIDs   = []string{"PID", "PV1", "OBX", "NTE", "ZXX"}
	genEscapes      = []string{`\F\`, `\S\`, `\T\`, `\R\`, `\E\`, `\X41\`}
)

func NewMessageGen(seed string) *MessageGen {
	return &MessageGen{rng: rand.New(rand.NewChaCha8(sha256.Sum256([]byte(seed))))} //nolint:gosec // G404 -- deterministic seeded generator for reproducible test inputs
}

func (g *MessageGen) Next() Generated {
	g.n++
	cid := fmt.Sprintf("P%06d%s", g.n, g.alnum(4))

	var b strings.Builder
	b.WriteString("MSH|^~\\&|" + g.alnum(1+g.rng.IntN(8)) + "|" + g.alnum(1+g.rng.IntN(8)) +
		"|RECVAPP|RECVFAC|20240101120000||" + g.pick(genMessageTypes) + "|" + cid + "|P|2.5")
	for range g.rng.IntN(6) {
		b.WriteString("\r" + g.pick(genSegmentIDs))
		for range 1 + g.rng.IntN(6) {
			b.WriteString("|" + g.field())
		}
	}

	return Generated{Raw: b.String(), ControlID: cid}
}

func (g *MessageGen) field() string {
	reps := make([]string, 1+g.rng.IntN(3))
	for i := range reps {
		comps := make([]string, 1+g.rng.IntN(3))
		for j := range comps {
			subs := make([]string, 1+g.rng.IntN(2))
			for k := range subs {
				subs[k] = g.text()
			}
			comps[j] = strings.Join(subs, "&")
		}
		reps[i] = strings.Join(comps, "^")
	}
	return strings.Join(reps, "~")
}

func (g *MessageGen) text() string {
	switch g.rng.IntN(4) {
	case 0:
		return ""
	case 1:
		return g.alnum(1+g.rng.IntN(6)) + g.pick(genEscapes) + g.alnum(g.rng.IntN(4))
	default:
		return g.alnum(1 + g.rng.IntN(12))
	}
}

func (g *MessageGen) alnum(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alnum[g.rng.IntN(len(alnum))]
	}
	return string(b)
}

func (g *MessageGen) pick(options []string) string {
	return options[g.rng.IntN(len(options))]
}
