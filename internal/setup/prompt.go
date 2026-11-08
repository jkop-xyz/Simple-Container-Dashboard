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
	// Pick lets the operator filter items by typing and choose one. It returns the
	// chosen index, or -1 when they'd rather enter something else.
	Pick(question string, items []string) int
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

// pickPage is how many matches Pick lists at once.
const pickPage = 15

// Pick implements Prompter: type part of a name to filter, then choose by number.
func (t *Terminal) Pick(question string, items []string) int {
	filter := ""
	for {
		var matches []int
		for i, it := range items {
			if strings.Contains(strings.ToLower(it), strings.ToLower(filter)) {
				matches = append(matches, i)
			}
		}
		fmt.Fprintln(t.Out, question)
		shown := matches
		if len(shown) > pickPage {
			shown = shown[:pickPage]
		}
		for n, i := range shown {
			fmt.Fprintf(t.Out, "  %d) %s\n", n+1, items[i])
		}
		if len(matches) > len(shown) {
			fmt.Fprintf(t.Out, "  … %d more; type to narrow the list\n", len(matches)-len(shown))
		}
		if len(matches) == 0 {
			fmt.Fprintln(t.Out, "  (no matches)")
		}
		fmt.Fprintln(t.Out, "  0) none of these: enter a URL instead")
		fmt.Fprint(t.Out, "Number, or text to filter: ")
		answer := t.line()
		if n, err := strconv.Atoi(answer); err == nil {
			if n == 0 {
				return -1
			}
			if n >= 1 && n <= len(shown) {
				return shown[n-1]
			}
			continue
		}
		filter = answer
	}
}
