package workspace_test

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/legacy/workspace"
)

func TestSessionMetaPath(t *testing.T) {
	got := workspace.SessionMetaPath("/root", "sess-X")
	if !strings.HasSuffix(got, "/sess-X/meta.json") {
		t.Fatalf("unexpected: %s", got)
	}
}

func TestFlatScope(t *testing.T) {
	read, write := workspace.FlatScope("/root", "sess-X")
	if len(read) != 1 || len(write) != 1 || read[0] != write[0] {
		t.Fatal("flat scope mismatch")
	}
}
