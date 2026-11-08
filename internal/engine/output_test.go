package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/engine"
)

func TestOutputsKeepTheEndOfLongOutput(t *testing.T) {
	o := engine.Outputs{Dir: t.TempDir()}
	var lines []string
	for i := range 20000 {
		lines = append(lines, strings.Repeat("x", 40)+" line "+string(rune('a'+i%26)))
	}
	lines = append(lines, "==> Apply finished")
	if err := o.Save("run-1", lines); err != nil {
		t.Fatal(err)
	}
	got, ok, err := o.Load("run-1")
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	size := len(strings.Join(got, "\n"))
	if size > engine.MaxOutput+100 || got[0] != "… (earlier output trimmed)" || got[len(got)-1] != "==> Apply finished" {
		t.Errorf("kept %d bytes, first %q, last %q", size, got[0], got[len(got)-1])
	}
	if _, ok, _ := o.Load("../etc/passwd"); ok {
		t.Error("unsafe id loaded")
	}
	if id := engine.NewApplyID(time.Date(2026, 11, 9, 18, 22, 0, 0, time.UTC), "abcdef1234"); id != "20261109T182200Z-abcdef1" {
		t.Errorf("id %q", id)
	}
}
