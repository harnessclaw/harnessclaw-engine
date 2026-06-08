package submittool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/legacy/workspace"
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

func TestSubmit_AcceptsStructuredResultWhenOutputSchemaSet(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "browser_weibo_trending"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{
		"type":     "object",
		"required": []string{"content", "source"},
		"properties": map[string]any{
			"content": map[string]any{"type": "string"},
			"source": map[string]any{
				"type": "string",
				"enum": []string{"direct_access", "search_fallback", "api_fallback", "partial"},
			},
		},
	}
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: workspace.SessionRoot(root, sid),
		TaskID:      tid,
		Agent:       "browser-agent",
	})
	ctx = tool.WithTaskContract(ctx, tool.TaskContract{
		TaskID:       tid,
		OutputSchema: schema,
	})

	raw, _ := json.Marshal(map[string]any{
		"task_id": tid,
		"result": map[string]any{
			"content": "微博热搜前 50 条",
			"source":  "direct_access",
		},
	})
	res, err := New().Execute(ctx, raw)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected structured result accept, got error: %s", res.Content)
	}
	if accepted, _ := res.Metadata[MetadataKeyAccepted].(bool); !accepted {
		t.Errorf("submission_accepted = false; want true")
	}
	if got, _ := res.Metadata["summary"].(string); got != "微博热搜前 50 条" {
		t.Errorf("summary = %q", got)
	}
	result, ok := res.Metadata["result"].(map[string]any)
	if !ok || result["source"] != "direct_access" {
		t.Fatalf("metadata result = %#v", res.Metadata["result"])
	}
}

func TestSubmit_RejectsStructuredResultAgainstOutputSchema(t *testing.T) {
	root := t.TempDir()
	sid, tid := "sess_a", "browser_bad"
	if err := workspace.EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: workspace.SessionRoot(root, sid),
		TaskID:      tid,
		Agent:       "browser-agent",
	})
	ctx = tool.WithTaskContract(ctx, tool.TaskContract{
		TaskID: tid,
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []string{"content", "source"},
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
				"source":  map[string]any{"type": "string"},
			},
		},
	})

	raw, _ := json.Marshal(map[string]any{
		"task_id": tid,
		"result":  map[string]any{"content": "missing source"},
	})
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatalf("schema-invalid result should reject: %+v", res)
	}
	if !strings.Contains(res.Content, "source") {
		t.Fatalf("error should mention missing source, got: %s", res.Content)
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

// task_id / meta_path moved out of the input schema — they come from
// ctx.AgentScope (TaskID + derived "tasks/{task_id}/meta.json"). The
// only client-side input that's still a hard error is an absolute
// meta_path, which would escape the session sandbox if accepted.
// Missing fields are validated lazily inside Execute against ctx.
func TestValidateInput_RejectsMalformed(t *testing.T) {
	tt := []struct {
		name      string
		raw       string
		wantError bool
	}{
		{"missing task_id", `{"meta_path":"tasks/x/meta.json"}`, false},
		{"missing meta_path", `{"task_id":"x"}`, false},
		{"empty task_id", `{"task_id":" ","meta_path":"tasks/x/meta.json"}`, false},
		{"empty meta_path", `{"task_id":"x","meta_path":"  "}`, false},
		{"empty body", `{}`, false},
		{"absolute path", `{"task_id":"x","meta_path":"/etc/passwd"}`, true},
	}
	sub := New()
	for _, c := range tt {
		t.Run(c.name, func(t *testing.T) {
			err := sub.ValidateInput(json.RawMessage(c.raw))
			if c.wantError && err == nil {
				t.Errorf("expected validation failure for %s", c.name)
			}
			if !c.wantError && err != nil {
				t.Errorf("unexpected validation failure for %s: %v", c.name, err)
			}
		})
	}
}
