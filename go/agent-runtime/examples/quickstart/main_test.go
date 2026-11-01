package main

import (
	"bytes"
	"testing"
)

func TestQuickstartShowsPlainToolDerivedAnswer(t *testing.T) {
	var output bytes.Buffer
	if err := run(&output); err != nil {
		t.Fatal(err)
	}
	const want = "Release checks: 12 passed, 0 failed.\nRun quickstart-release-report: completed\n"
	if got := output.String(); got != want {
		t.Fatalf("unexpected quickstart output:\ngot:  %q\nwant: %q", got, want)
	}
}
