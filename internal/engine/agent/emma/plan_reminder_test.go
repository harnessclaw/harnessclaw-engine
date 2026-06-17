package emma

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestActivePlanStore_SetGetClear(t *testing.T) {
	store := NewActivePlanStore()

	// 空 store Get 返回 false
	if _, ok := store.Get("sid"); ok {
		t.Error("empty store should not return any plan")
	}

	// Set 后 Get 命中，字段对得上
	store.Set("sid", "/tmp/plan.md", "t-123")
	plan, ok := store.Get("sid")
	if !ok {
		t.Fatal("Get should return ok after Set")
	}
	if plan.Path != "/tmp/plan.md" {
		t.Errorf("Path = %q, want %q", plan.Path, "/tmp/plan.md")
	}
	if plan.TaskID != "t-123" {
		t.Errorf("TaskID = %q, want %q", plan.TaskID, "t-123")
	}
	if plan.CreatedAt.IsZero() {
		t.Error("CreatedAt should be auto-stamped")
	}

	// 后写覆盖前写
	store.Set("sid", "/tmp/plan2.md", "t-456")
	plan, _ = store.Get("sid")
	if plan.Path != "/tmp/plan2.md" {
		t.Errorf("Set should overwrite; Path = %q", plan.Path)
	}

	// Clear 后 Get 返回 false
	store.Clear("sid")
	if _, ok := store.Get("sid"); ok {
		t.Error("Clear should remove the plan")
	}
}

func TestActivePlanStore_SetGuards(t *testing.T) {
	store := NewActivePlanStore()

	// 空 sessionID / 空 path 都 noop
	store.Set("", "/tmp/plan.md", "t-1")
	store.Set("sid", "", "t-1")
	if _, ok := store.Get(""); ok {
		t.Error("empty sessionID should not be stored")
	}
	if _, ok := store.Get("sid"); ok {
		t.Error("empty path should not be stored")
	}

	// nil store 不 panic
	var nilStore *ActivePlanStore
	nilStore.Set("sid", "/tmp/p", "t-1")
	if _, ok := nilStore.Get("sid"); ok {
		t.Error("nil store should not return anything")
	}
	nilStore.Clear("sid")
}

func TestActivePlanStore_BuildReminder(t *testing.T) {
	store := NewActivePlanStore()

	// 无注册时返回空
	if r := store.BuildReminder("sid"); r != "" {
		t.Errorf("BuildReminder with no active plan should be empty, got %q", r)
	}

	// 写一个真实文件
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	planBody := "# 计划:test\n\n> 目标:test\n> 边界:test\n\n## T1 · 测试\n\n### 子任务\nfoo\n- [ ] 待办一\n"
	if err := os.WriteFile(planPath, []byte(planBody), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	store.Set("sid", planPath, "t-1")
	rem := store.BuildReminder("sid")
	if rem == "" {
		t.Fatal("BuildReminder should return non-empty when plan exists")
	}
	if !strings.Contains(rem, planPath) {
		t.Errorf("reminder should contain plan path %q, got: %q", planPath, rem)
	}
	if !strings.Contains(rem, planBody) {
		t.Errorf("reminder should contain plan body")
	}
	if !strings.HasPrefix(rem, "<plan-reminder>") {
		t.Errorf("reminder should start with <plan-reminder> tag")
	}
	if !strings.HasSuffix(rem, "</plan-reminder>") {
		t.Errorf("reminder should end with </plan-reminder> tag")
	}
}

func TestActivePlanStore_BuildReminder_FileMissing(t *testing.T) {
	store := NewActivePlanStore()
	store.Set("sid", "/nonexistent/path/plan.md", "t-1")

	rem := store.BuildReminder("sid")
	if rem != "" {
		t.Errorf("BuildReminder should return empty when file is missing")
	}

	// 应该顺手清理 store
	if _, ok := store.Get("sid"); ok {
		t.Error("BuildReminder should Clear the store when file is unreadable")
	}
}

func TestInjectPlanReminder_PrependsToExistingText(t *testing.T) {
	msg := &types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "按计划执行"},
		},
	}
	injectPlanReminder(msg, "<plan-reminder>X</plan-reminder>")

	if len(msg.Content) != 1 {
		t.Fatalf("Content count = %d, want 1", len(msg.Content))
	}
	want := "<plan-reminder>X</plan-reminder>\n\n按计划执行"
	if msg.Content[0].Text != want {
		t.Errorf("Text = %q, want %q", msg.Content[0].Text, want)
	}
}

func TestInjectPlanReminder_PrependsBeforeNonText(t *testing.T) {
	// 纯多模态附件场景：没有 text block 时应该插一个 text block 在最前面
	msg := &types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Text: ""},
		},
	}
	injectPlanReminder(msg, "<plan-reminder>X</plan-reminder>")

	if len(msg.Content) != 2 {
		t.Fatalf("Content count = %d, want 2 (prepended text + image)", len(msg.Content))
	}
	if msg.Content[0].Type != types.ContentTypeText {
		t.Errorf("first block should be text, got %s", msg.Content[0].Type)
	}
	if msg.Content[0].Text != "<plan-reminder>X</plan-reminder>" {
		t.Errorf("first block text = %q", msg.Content[0].Text)
	}
	if msg.Content[1].Type != types.ContentTypeImage {
		t.Errorf("second block should still be the original image")
	}
}

func TestInjectPlanReminder_PrependsToFirstTextWhenMixed(t *testing.T) {
	msg := &types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeImage, Text: ""},
			{Type: types.ContentTypeText, Text: "请按计划处理"},
		},
	}
	injectPlanReminder(msg, "<plan-reminder>X</plan-reminder>")

	if len(msg.Content) != 2 {
		t.Fatalf("Content count = %d, want 2", len(msg.Content))
	}
	// image 应保留原位（不被前置覆盖）
	if msg.Content[0].Type != types.ContentTypeImage {
		t.Errorf("image block should remain at index 0")
	}
	// 第二个 text block 应被前置
	want := "<plan-reminder>X</plan-reminder>\n\n请按计划处理"
	if msg.Content[1].Text != want {
		t.Errorf("text block text = %q, want %q", msg.Content[1].Text, want)
	}
}

func TestInjectPlanReminder_EmptyReminderNoop(t *testing.T) {
	msg := &types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "hello"},
		},
	}
	injectPlanReminder(msg, "")
	if msg.Content[0].Text != "hello" {
		t.Errorf("empty reminder should leave message untouched, got %q", msg.Content[0].Text)
	}
}

func TestInjectPlanReminder_NilMessageNoPanic(t *testing.T) {
	// 防御性测试：nil msg 不应 panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("injectPlanReminder(nil) panicked: %v", r)
		}
	}()
	injectPlanReminder(nil, "<plan-reminder>X</plan-reminder>")
}
