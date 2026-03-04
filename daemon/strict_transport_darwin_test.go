//go:build darwin

package daemon

import (
	"reflect"
	"sync"
	"testing"
)

type fakePFRunner struct {
	runCalls    [][]string
	outputCalls [][]string
	anchorsOut  string
}

func (runner *fakePFRunner) Run(args []string, _ string) error {
	copied := append([]string(nil), args...)
	runner.runCalls = append(runner.runCalls, copied)
	return nil
}

func (runner *fakePFRunner) Output(args []string) (string, error) {
	copied := append([]string(nil), args...)
	runner.outputCalls = append(runner.outputCalls, copied)
	return runner.anchorsOut, nil
}

func TestParsePFAnchorsFiltersPrefix(t *testing.T) {
	anchors := parsePFAnchors("\ncom.klitka.vm/a\nother.anchor\ncom.klitka.vm/b\ncom.klitka.vm/a\n", "com.klitka.vm/")
	want := []string{"com.klitka.vm/a", "com.klitka.vm/b"}
	if !reflect.DeepEqual(anchors, want) {
		t.Fatalf("anchors = %v, want %v", anchors, want)
	}
}

func TestPrepareStrictTransportDarwinCleansStaleAnchors(t *testing.T) {
	runner := &fakePFRunner{anchorsOut: "com.klitka.vm/stale-a\ncom.klitka.vm/stale-b\nother.anchor\n"}

	originalRunner := newPFRunner
	originalOnce := strictPFPrepareOnce
	originalErr := strictPFPrepareErr
	newPFRunner = func() pfCommandRunner { return runner }
	strictPFPrepareOnce = sync.Once{}
	strictPFPrepareErr = nil
	t.Cleanup(func() {
		newPFRunner = originalRunner
		strictPFPrepareOnce = originalOnce
		strictPFPrepareErr = originalErr
	})

	if err := prepareStrictTransportDarwin(); err != nil {
		t.Fatalf("prepareStrictTransportDarwin failed: %v", err)
	}

	if len(runner.runCalls) < 3 {
		t.Fatalf("expected at least 3 run calls, got %d", len(runner.runCalls))
	}
	if !reflect.DeepEqual(runner.runCalls[0], []string{"-E"}) {
		t.Fatalf("first run call = %v, want [-E]", runner.runCalls[0])
	}
	if !reflect.DeepEqual(runner.outputCalls, [][]string{{"-s", "Anchors"}}) {
		t.Fatalf("output calls = %v", runner.outputCalls)
	}

	gotFlush := map[string]bool{}
	for _, call := range runner.runCalls[1:] {
		if len(call) == 4 && call[0] == "-a" && call[2] == "-F" && call[3] == "all" {
			gotFlush[call[1]] = true
		}
	}
	if !gotFlush["com.klitka.vm/stale-a"] || !gotFlush["com.klitka.vm/stale-b"] {
		t.Fatalf("expected stale anchors to be flushed, got %v", gotFlush)
	}
}

func TestPrepareStrictTransportDarwinRunsOnce(t *testing.T) {
	runner := &fakePFRunner{anchorsOut: ""}

	originalRunner := newPFRunner
	originalOnce := strictPFPrepareOnce
	originalErr := strictPFPrepareErr
	newPFRunner = func() pfCommandRunner { return runner }
	strictPFPrepareOnce = sync.Once{}
	strictPFPrepareErr = nil
	t.Cleanup(func() {
		newPFRunner = originalRunner
		strictPFPrepareOnce = originalOnce
		strictPFPrepareErr = originalErr
	})

	if err := prepareStrictTransportDarwin(); err != nil {
		t.Fatalf("first prepare failed: %v", err)
	}
	if err := prepareStrictTransportDarwin(); err != nil {
		t.Fatalf("second prepare failed: %v", err)
	}

	if len(runner.outputCalls) != 1 {
		t.Fatalf("expected one output call due to once semantics, got %d", len(runner.outputCalls))
	}
}
