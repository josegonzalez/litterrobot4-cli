package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// prompter reads answers from standard input.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	// secret reads a line without echo. Tests replace it.
	secret func(ctx context.Context, prompt string) (string, error)
}

func newPrompter(out io.Writer) *prompter {
	p := &prompter{in: bufio.NewReader(os.Stdin), out: out}
	p.secret = p.readTerminalSecret
	return p
}

// ask prints a prompt and reads a line, returning early on cancellation.
func (p *prompter) ask(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := p.in.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(p.out)
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil && !(errors.Is(r.err, io.EOF) && r.line != "") {
			if errors.Is(r.err, io.EOF) {
				return "", errors.New("answers are needed on standard input")
			}
			return "", r.err
		}
		return strings.TrimRight(r.line, "\r\n"), nil
	}
}

// confirm asks a yes or no question.
func (p *prompter) confirm(ctx context.Context, prompt string, def bool) (bool, error) {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	for {
		a, err := p.ask(ctx, prompt+hint)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.out, "Please answer y or n.")
	}
}

func (p *prompter) readSecret(ctx context.Context, prompt string) (string, error) {
	return p.secret(ctx, prompt)
}

// readTerminalSecret reads without echo when stdin is a terminal.
func (p *prompter) readTerminalSecret(ctx context.Context, prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return p.ask(ctx, prompt)
	}
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	fmt.Fprint(p.out, prompt)
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		ch <- result{b, err}
	}()
	select {
	case <-ctx.Done():
		term.Restore(fd, state)
		fmt.Fprintln(p.out)
		return "", ctx.Err()
	case r := <-ch:
		fmt.Fprintln(p.out)
		return string(r.b), r.err
	}
}
