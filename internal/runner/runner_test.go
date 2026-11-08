package runner_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

func TestExecCapturesAndStreamsOutput(t *testing.T) {
	var live bytes.Buffer
	res, err := runner.Exec{}.Run(context.Background(), runner.Cmd{
		Name: "sh", Args: []string{"-c", "echo out; echo err >&2"}, Output: &live,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(res.Output), live.String()} {
		if !strings.Contains(s, "out") || !strings.Contains(s, "err") {
			t.Errorf("output %q missing stdout or stderr", s)
		}
	}
}

func TestExecNonZeroExit(t *testing.T) {
	res, err := runner.Exec{}.Run(context.Background(), runner.Cmd{Name: "sh", Args: []string{"-c", "echo nope; exit 3"}})
	var exitErr *runner.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("got %v, want *ExitError", err)
	}
	if res.ExitCode != 3 || !strings.Contains(err.Error(), "nope") {
		t.Errorf("exit %d, err %v", res.ExitCode, err)
	}
}

func TestExecMissingBinary(t *testing.T) {
	_, err := runner.Exec{}.Run(context.Background(), runner.Cmd{Name: "scd-definitely-not-a-binary"})
	var exitErr *runner.ExitError
	if err == nil || errors.As(err, &exitErr) {
		t.Fatalf("got %v, want a start error", err)
	}
}

func TestFakeRecordsAndScripts(t *testing.T) {
	f := &runnertest.Fake{}
	f.On("docker compose", runnertest.Response{Output: "ok"})
	f.On("docker compose -p broken", runnertest.Response{ExitCode: 1, Output: "boom"})

	ctx := context.Background()
	var live bytes.Buffer
	res, err := f.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "-p", "app", "up"}, Output: &live})
	if err != nil || string(res.Output) != "ok" || live.String() != "ok" {
		t.Errorf("app: res %q live %q err %v", res.Output, live.String(), err)
	}
	if _, err := f.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "-p", "broken", "up"}}); err == nil {
		t.Error("broken: want error")
	}
	if _, err := f.Run(ctx, runner.Cmd{Name: "git", Args: []string{"status"}}); err != nil {
		t.Errorf("unscripted command should succeed, got %v", err)
	}

	want := []string{"docker compose -p app up", "docker compose -p broken up", "git status"}
	if got := f.Lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Lines() = %q, want %q", got, want)
	}
}
