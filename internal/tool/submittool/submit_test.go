package submittool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

func seedMeta(t *testing.T, root, sid, tid string, m *workspace.Meta) {
	t.Helper()
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureTaskDir(root, sid, tid); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(workspace.MetaPath(root, sid, tid), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func ctxWithSession(root, sid string) context.Context {
	return tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
}

func TestSubmit_HappyPath(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	meta := &workspace.Meta{
		TaskID: tid, Agent: "writer",
		Status: workspace.StatusDone, Summary: "3 个发现",
	}
	seedMeta(t, root, sid, tid, meta)

	raw, _ := json.Marshal(map[string]any{"task_id": tid, "meta_path": "tasks/" + tid + "/meta.json"})
	res, err := New().Execute(ctxWithSession(root, sid), raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected accept, got error: %s", res.Content)
	}
	if got, _ := res.Metadata["render_hint"].(string); got != MetadataRenderHint {
		t.Errorf("render_hint = %q, want %q", got, MetadataRenderHint)
	}
	if accepted, _ := res.Metadata[MetadataKeyAccepted].(bool); !accepted {
		t.Errorf("submission_accepted = false; want true")
	}
	if !strings.Contains(res.Content, "3 个发现") {
		t.Errorf("Content should carry the meta summary; got: %s", res.Content)
	}
}

func TestSubmit_RejectsAbsoluteMetaPath(t *testing.T) {
	root := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"task_id": "t_001", "meta_path": filepath.Join(root, "absolute.json")})
	if err := New().ValidateInput(raw); err == nil {
		t.Errorf("absolute meta_path must fail ValidateInput")
	}
}

func TestSubmit_RejectsMissingMeta(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"task_id": tid, "meta_path": "tasks/" + tid + "/meta.json"})
	res, _ := New().Execute(ctxWithSession(root, sid), raw)
	if !res.IsError {
		t.Errorf("missing meta.json should reject; got %+v", res)
	}
	if res.ErrorType != types.ToolErrorContractFail {
		t.Errorf("expected contract_fail, got %s", res.ErrorType)
	}
}

func TestSubmit_RejectsMetaTaskIDMismatch(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	seedMeta(t, root, sid, tid, &workspace.Meta{
		TaskID: "DIFFERENT", Agent: "writer",
		Status: workspace.StatusDone, Summary: "x",
	})
	raw, _ := json.Marshal(map[string]any{"task_id": tid, "meta_path": "tasks/" + tid + "/meta.json"})
	res, _ := New().Execute(ctxWithSession(root, sid), raw)
	if !res.IsError {
		t.Errorf("task_id mismatch should reject; got %+v", res)
	}
}

func TestSubmit_RejectsInvalidMetaSchema(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "t_001"
	// status=running is invalid for meta.json (must be done|failed)
	seedMeta(t, root, sid, tid, &workspace.Meta{
		TaskID: tid, Agent: "writer",
		Status: workspace.StatusRunning, Summary: "x",
	})
	raw, _ := json.Marshal(map[string]any{"task_id": tid, "meta_path": "tasks/" + tid + "/meta.json"})
	res, _ := New().Execute(ctxWithSession(root, sid), raw)
	if !res.IsError {
		t.Errorf("invalid meta should reject; got %+v", res)
	}
}

func TestValidateInput_RejectsMalformed(t *testing.T) {
	tt := []struct {
		name string
		raw  string
	}{
		{"missing task_id", `{"meta_path":"tasks/x/meta.json"}`},
		{"missing meta_path", `{"task_id":"x"}`},
		{"empty task_id", `{"task_id":" ","meta_path":"tasks/x/meta.json"}`},
		{"empty meta_path", `{"task_id":"x","meta_path":"  "}`},
		{"absolute path", `{"task_id":"x","meta_path":"/etc/passwd"}`},
	}
	sub := New()
	for _, c := range tt {
		t.Run(c.name, func(t *testing.T) {
			if err := sub.ValidateInput(json.RawMessage(c.raw)); err == nil {
				t.Errorf("expected validation failure for %s", c.name)
			}
		})
	}
}
