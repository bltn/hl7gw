package configure

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/bltn/hl7gw/internal/console"
)

type prompter struct {
	in   *bufio.Reader
	out  io.Writer
	term console.Terminal
	err  error
}

func (p *prompter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.out, format, args...)
}

func (p *prompter) heading(title string) {
	p.printf("\n%s\n", title)
}

func (p *prompter) ask(label, def string, check func(string) error) string {
	for p.err == nil {
		p.printf("  %s%s: ", label, p.hint(def))
		line, err := p.in.ReadString('\n')
		if line == "" && err != nil {
			p.err = err
			if errors.Is(err, io.EOF) {
				p.err = ErrInputClosed
			}
			return ""
		}
		if !p.term.TTY {
			p.printf("\n")
		}
		answer := strings.TrimRight(line, "\r\n")
		if answer == "" {
			answer = def
		}
		if err := check(answer); err != nil {
			p.printf("  %s\n", p.term.Alert(err.Error()))
			continue
		}
		return answer
	}
	return ""
}

func (p *prompter) hint(def string) string {
	if def == "" {
		return ""
	}
	return " [" + p.term.Dim(def) + "]"
}

func (p *prompter) askInt(label string, def int, check func(int) error) int {
	answer := p.ask(label, strconv.Itoa(def), func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil {
			return errors.New("must be a whole number")
		}
		return check(n)
	})
	n, _ := strconv.Atoi(answer)
	return n
}

func (p *prompter) choose(label string, options []string, def string) string {
	return strings.ToLower(p.ask(label+" ("+strings.Join(options, "/")+")", def, func(s string) error {
		if !slices.Contains(options, strings.ToLower(s)) {
			return errors.New("must be one of " + strings.Join(options, ", "))
		}
		return nil
	}))
}

func (p *prompter) confirm(label string) bool {
	answer := p.ask(label+" [y/N]", "", func(s string) error {
		if !slices.Contains([]string{"", "y", "yes", "n", "no"}, strings.ToLower(s)) {
			return errors.New("answer y or n")
		}
		return nil
	})
	return strings.HasPrefix(strings.ToLower(answer), "y")
}

func positive(n int) error {
	if n <= 0 {
		return errors.New("must be a positive number")
	}
	return nil
}

func port(n int) error {
	if n < 1 || n > maxPort {
		return fmt.Errorf("must be a port between 1 and %d", maxPort)
	}
	return nil
}

func envName(s string) error {
	if !envNamePattern.MatchString(s) {
		return errors.New("must be an environment variable name: letters, digits and _, not starting with a digit")
	}
	return nil
}

func nonEmpty(s string) error {
	if s == "" {
		return errors.New("must not be empty")
	}
	return nil
}
