package setup

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Prompter asks the operator questions. Tests script the answers.
type Prompter interface {
	// Confirm asks a yes/no question with a default.
	Confirm(question string, def bool) bool
	// Choose asks to pick one of options and returns its index.
	Choose(question string, options []string) int
	// Input asks for free text, returning def for an empty answer.
	Input(question, def string) string
}

// Terminal prompts on a terminal.
type Terminal struct {
	In  *bufio.Reader
	Out io.Writer
}

// NewTerminal returns a prompter reading in and writing out.
func NewTerminal(in io.Reader, out io.Writer) *Terminal {
	return &Terminal{In: bufio.NewReader(in), Out: out}
}

func (t *Terminal) line() string {
	s, _ := t.In.ReadString('\n')
	return strings.TrimSpace(s)
}

// Confirm implements Prompter.
func (t *Terminal) Confirm(question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		fmt.Fprintf(t.Out, "%s %s ", question, hint)
		switch strings.ToLower(t.line()) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

// Choose implements Prompter.
func (t *Terminal) Choose(question string, options []string) int {
	fmt.Fprintln(t.Out, question)
	for i, o := range options {
		fmt.Fprintf(t.Out, "  %d) %s\n", i+1, o)
	}
	for {
		fmt.Fprintf(t.Out, "Choose 1-%d: ", len(options))
		if n, err := strconv.Atoi(t.line()); err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
	}
}

// Input implements Prompter.
func (t *Terminal) Input(question, def string) string {
	if def != "" {
		fmt.Fprintf(t.Out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(t.Out, "%s: ", question)
	}
	if s := t.line(); s != "" {
		return s
	}
	return def
}
