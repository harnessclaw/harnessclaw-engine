package videogen

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const generatedDirName = "generated"

func errResult(msg string, errType types.ToolErrorType) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: errType}
}

// resolveSessionRoot mirrors imagegen: prefer AgentScope.SessionRoot, then fall
// back to ArtifactProducer.SessionID + rootDir.
func resolveSessionRoot(ctx context.Context, rootDir string) (string, error) {
	scope, ok := tool.AgentScopeFromCtx(ctx)
	if ok && strings.TrimSpace(scope.SessionRoot) != "" {
		return scope.SessionRoot, nil
	}
	producer, ok := tool.GetArtifactProducer(ctx)
	if ok && strings.TrimSpace(producer.SessionID) != "" && strings.TrimSpace(rootDir) != "" {
		return workspace.SessionRoot(rootDir, producer.SessionID), nil
	}
	return "", errors.New("SessionRoot missing in ctx — engine configuration error")
}

// resolveOutDir 决定视频下载落在哪：
//   - 有 AgentScope.TaskID（sub-agent 正常派活路径，包括 content_creator）
//     → {sessionRoot}/tasks/{task_id}/  ← emma 的 promote 工具能找到
//   - 无 TaskID（root agent 直调 / 测试 / 旧 spawn）
//     → {sessionRoot}/generated/        ← 保留原有 session 共享池语义
func resolveOutDir(ctx context.Context, sessionRoot string) string {
	if scope, ok := tool.AgentScopeFromCtx(ctx); ok && strings.TrimSpace(scope.TaskID) != "" {
		return filepath.Join(sessionRoot, "tasks", scope.TaskID)
	}
	return filepath.Join(sessionRoot, generatedDirName)
}

// emitDeliverable does a non-blocking deliverable emit (same pattern as imagegen).
func emitDeliverable(ctx context.Context, filePath string, byteSize int) {
	out, ok := tool.GetEventOut(ctx)
	if !ok || out == nil {
		return
	}
	select {
	case out <- types.EngineEvent{
		Type:        types.EngineEventDeliverable,
		Deliverable: &types.Deliverable{FilePath: filePath, ByteSize: byteSize},
	}:
	default:
	}
}
