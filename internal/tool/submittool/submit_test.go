package submittool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// helper: build a context wired with the given store + contract.
func newTestCtx(store artifact.Store, contract tool.TaskContract) context.Context {
	ctx := context.Background()
	ctx = tool.WithArtifactStoreValue(ctx, store)
	ctx = tool.WithTaskContract(ctx, contract)
	return ctx
}

// helper: write an artifact for THIS task (matching contract.TaskID).
func seedTaskArtifact(t *testing.T, store artifact.Store, taskID string, role string, size int) *artifact.Artifact {
	t.Helper()
	content := strings.Repeat("x", size)
	a, err := store.Save(context.Background(), &artifact.SaveInput{
		Type:        artifact.TypeFile,
		Name:        role + ".md",
		Description: role + " for test",
		Content:     content,
		Producer:    artifact.Producer{AgentID: "agent_l3", TaskID: taskID},
	})
	if err != nil {
		t.Fatalf("seed save: %v", err)
	}
	return a
}

func TestValidateInput_RejectsMalformed(t *testing.T) {
	tt := []struct {
		name string
		raw  string
	}{
		{"empty artifacts", `{"artifacts":[],"summary":"x"}`},
		{"missing summary", `{"artifacts":[{"artifact_id":"art_000000000000000000000000","role":"r"}]}`},
		{"empty summary", `{"artifacts":[{"artifact_id":"art_000000000000000000000000","role":"r"}],"summary":"  "}`},
		{"summary too long", `{"artifacts":[{"artifact_id":"art_000000000000000000000000","role":"r"}],"summary":"` + strings.Repeat("x", 201) + `"}`},
		{"malformed id", `{"artifacts":[{"artifact_id":"art_short","role":"r"}],"summary":"ok"}`},
		{"missing role", `{"artifacts":[{"artifact_id":"art_000000000000000000000000","role":""}],"summary":"ok"}`},
	}
	tool := New()
	for _, c := range tt {
		t.Run(c.name, func(t *testing.T) {
			if err := tool.ValidateInput(json.RawMessage(c.raw)); err == nil {
				t.Errorf("expected validation failure for %s; got nil", c.name)
			}
		})
	}
}

func TestExecute_RejectsClaimedNonExistentID(t *testing.T) {
	// Failure mode #2: LLM fabricates an ID. Server-side store lookup
	// must catch this even when the input passes shape validation.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	contract := tool.TaskContract{TaskID: "task_a"}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"art_000000000000000000000000","role":"report"}],
		"summary":"all good"
	}`)
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatalf("expected rejection of fabricated ID; got success: %s", res.Content)
	}
	if !strings.Contains(res.Content, "not found") {
		t.Errorf("rejection should explain 'not found'; got %q", res.Content)
	}
}

func TestExecute_RejectsForeignTaskArtifact(t *testing.T) {
	// Failure mode #8: claiming an artifact someone else produced.
	// Producer.task_id stamp + M4 lineage check defends against this.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	foreign := seedTaskArtifact(t, store, "task_OTHER", "report", 100)

	contract := tool.TaskContract{TaskID: "task_THIS"}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + foreign.ID + `","role":"report"}],
		"summary":"submitting other task's artifact"
	}`)
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatal("expected rejection of foreign-task artifact")
	}
	if !strings.Contains(res.Content, "task_id") {
		t.Errorf("rejection should mention task_id mismatch; got %q", res.Content)
	}
}

func TestExecute_RejectsTimeTravel(t *testing.T) {
	// Subtle attack: artifact was written before this task even started.
	// MUST be rejected even if everything else matches — otherwise an
	// L3 could "complete" instantly by claiming a pre-existing record.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_THIS", "report", 100)

	// Task started 1h AFTER the artifact was created.
	contract := tool.TaskContract{
		TaskID:        "task_THIS",
		TaskStartedAt: time.Now().Add(1 * time.Hour),
	}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + a.ID + `","role":"report"}],
		"summary":"x"
	}`)
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatal("expected rejection of pre-task artifact")
	}
	if !strings.Contains(res.Content, "precedes task start") {
		t.Errorf("rejection should mention temporal violation; got %q", res.Content)
	}
}

func TestExecute_RejectsMissingRequiredRole(t *testing.T) {
	// Failure mode #5 (partial submit): contract says required: report,
	// table — submitting only report leaves the contract violated.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_THIS", "report", 100)

	contract := tool.TaskContract{
		TaskID: "task_THIS",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "report", Required: true},
			{Role: "table", Required: true},
		},
	}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + a.ID + `","role":"report"}],
		"summary":"only report"
	}`)
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatalf("expected rejection of partial submission")
	}
	if !strings.Contains(res.Content, "table") {
		t.Errorf("rejection should name the missing role; got %q", res.Content)
	}
}

func TestExecute_RejectsTypeMismatch(t *testing.T) {
	// Failure mode #7: wrote wrong format. Contract says structured;
	// L3 wrote a file. Must reject with a hint about the mismatch.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_THIS", "report", 100) // type=file

	contract := tool.TaskContract{
		TaskID: "task_THIS",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "report", Type: "structured", Required: true},
		},
	}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + a.ID + `","role":"report"}],
		"summary":"wrong type"
	}`)
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatal("expected rejection of type mismatch")
	}
	if !strings.Contains(res.Content, "type") {
		t.Errorf("rejection should mention type mismatch; got %q", res.Content)
	}
}

func TestExecute_RejectsBelowMinSize(t *testing.T) {
	// Failure mode #3: placeholder write (size below min).
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_THIS", "report", 50)

	contract := tool.TaskContract{
		TaskID: "task_THIS",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "report", MinSizeBytes: 200, Required: true},
		},
	}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + a.ID + `","role":"report"}],
		"summary":"too small"
	}`)
	res, _ := New().Execute(ctx, raw)
	if !res.IsError {
		t.Fatal("expected rejection of undersize submission")
	}
	if !strings.Contains(res.Content, "size") {
		t.Errorf("rejection should mention size; got %q", res.Content)
	}
}

func TestExecute_AcceptsValidSubmission(t *testing.T) {
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_THIS", "report", 500)
	b := seedTaskArtifact(t, store, "task_THIS", "table", 200)

	contract := tool.TaskContract{
		TaskID:        "task_THIS",
		TaskStartedAt: time.Now().Add(-1 * time.Hour),
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "report", Type: "file", MinSizeBytes: 100, Required: true},
			{Role: "table", Type: "file", MinSizeBytes: 100, Required: true},
		},
	}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[
			{"artifact_id":"` + a.ID + `","role":"report"},
			{"artifact_id":"` + b.ID + `","role":"table"}
		],
		"summary":"clean submission"
	}`)
	res, _ := New().Execute(ctx, raw)
	if res.IsError {
		t.Fatalf("expected acceptance; got error: %s", res.Content)
	}
	// Verify M3 metadata signal — the loop reads this.
	if hint, _ := res.Metadata["render_hint"].(string); hint != MetadataRenderHint {
		t.Errorf("metadata render_hint = %q, want %q", hint, MetadataRenderHint)
	}
	if accepted, _ := res.Metadata[MetadataKeyAccepted].(bool); !accepted {
		t.Error("metadata submission_accepted should be true")
	}
	refs, _ := res.Metadata[MetadataKeyArtifacts].([]types.ArtifactRef)
	if len(refs) != 2 {
		t.Fatalf("metadata submitted_artifacts: want 2 refs, got %d", len(refs))
	}
	// Roles must be carried through so the parent integration step
	// knows what each ref maps to in the contract.
	roleSet := map[string]bool{}
	for _, r := range refs {
		roleSet[r.Role] = true
	}
	for _, want := range []string{"report", "table"} {
		if !roleSet[want] {
			t.Errorf("Ref for role %q missing", want)
		}
	}
}

func TestExecute_OptionalRoleNotSubmittedIsOk(t *testing.T) {
	// Required=false roles can be omitted without rejection — guards
	// against over-strictness when contract has nice-to-have fields.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_THIS", "report", 200)

	contract := tool.TaskContract{
		TaskID: "task_THIS",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "report", Required: true},
			{Role: "appendix", Required: false},
		},
	}
	ctx := newTestCtx(store, contract)

	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + a.ID + `","role":"report"}],
		"summary":"only required"
	}`)
	res, _ := New().Execute(ctx, raw)
	if res.IsError {
		t.Errorf("expected acceptance when only required submitted; got %q", res.Content)
	}
}

func TestExecute_NoContractDegradesToExistenceCheck(t *testing.T) {
	// When the contract is empty (zero-value TaskContract), only the
	// "exists + non-zero size" checks fire. Useful for tests / legacy
	// dispatches that haven't migrated to expected_outputs yet.
	store := artifact.NewMemoryStore(artifact.DefaultConfig())
	a := seedTaskArtifact(t, store, "task_X", "any", 100)

	ctx := newTestCtx(store, tool.TaskContract{}) // no contract
	raw := json.RawMessage(`{
		"artifacts":[{"artifact_id":"` + a.ID + `","role":"any"}],
		"summary":"degraded path"
	}`)
	res, _ := New().Execute(ctx, raw)
	if res.IsError {
		t.Errorf("no-contract path should accept any existing artifact; got %q", res.Content)
	}
}
