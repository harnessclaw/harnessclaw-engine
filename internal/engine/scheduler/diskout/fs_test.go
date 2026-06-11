package diskout

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	pkgtypes "harnessclaw-go/pkg/types"
)

func TestFS_OpenAppendReader(t *testing.T) {
	dir := t.TempDir()
	s := NewFS(dir)

	w, err := s.Open("t-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(pkgtypes.EngineEvent{Type: pkgtypes.EngineEventType("first")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(pkgtypes.EngineEvent{Type: pkgtypes.EngineEventType("second")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if got, want := s.Path("t-1"), filepath.Join(dir, "t-1.jsonl"); got != want {
		t.Errorf("Path got %q want %q", got, want)
	}

	r, err := s.Reader("t-1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	if !bytes.Contains(b, []byte(`"first"`)) || !bytes.Contains(b, []byte(`"second"`)) {
		t.Errorf("Reader content: %s", string(b))
	}
}

func TestFS_Tail(t *testing.T) {
	dir := t.TempDir()
	s := NewFS(dir)

	w, _ := s.Open("t-1")
	_ = w.Append(pkgtypes.EngineEvent{Type: pkgtypes.EngineEventType("a")})

	r, _ := s.Reader("t-1")
	defer r.Close()
	out := make(chan pkgtypes.EngineEvent, 4)
	ctx, cancel := context.WithCancel(context.Background())

	go s.Tail(ctx, "t-1", r, out)

	time.Sleep(20 * time.Millisecond) // 让 Tail 读完已有
	_ = w.Append(pkgtypes.EngineEvent{Type: pkgtypes.EngineEventType("b")})
	time.Sleep(150 * time.Millisecond)
	cancel()
	_ = w.Close()

	var got []pkgtypes.EngineEventType
	for evt := range out {
		got = append(got, evt.Type)
	}
	if len(got) < 2 {
		t.Errorf("Tail got %v want at least 2 events", got)
	}
}

func TestFS_AppendBlock_AsTextEvent(t *testing.T) {
	dir := t.TempDir()
	s := NewFS(dir)
	w, _ := s.Open("t-1")
	if err := w.AppendBlock(pkgtypes.ContentBlock{Type: pkgtypes.ContentTypeText, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	r, _ := s.Reader("t-1")
	defer r.Close()
	b, _ := io.ReadAll(r)
	// AppendBlock 编码成 EngineEventText 文本帧
	if !bytes.Contains(b, []byte(`"hello"`)) {
		t.Errorf("AppendBlock content not in file: %s", string(b))
	}
}
