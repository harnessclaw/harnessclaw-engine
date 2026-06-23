package videogen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
	"go.uber.org/zap"
)

const (
	ToolNameVideoQuery   = "video_query"
	defaultQueryTimeoutS = 3
	maxQueryTimeoutS     = 60
	pollInterval         = time.Second
)

type VideoQueryTool struct {
	tool.BaseTool
	source   ConfigSource
	registry *ProviderRegistry
	rootDir  string
	logger   *zap.Logger

	now   func() time.Time
	sleep func(time.Duration)
}

func NewQuery(source ConfigSource, registry *ProviderRegistry, rootDir string, logger *zap.Logger) *VideoQueryTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &VideoQueryTool{
		source:   source,
		registry: registry,
		rootDir:  rootDir,
		logger:   logger.Named("video_query"),
		now:      time.Now,
		sleep:    time.Sleep,
	}
}

func (*VideoQueryTool) Name() string                 { return ToolNameVideoQuery }
func (*VideoQueryTool) Description() string           { return videoQueryDescription }
func (*VideoQueryTool) IsReadOnly() bool              { return false }
func (*VideoQueryTool) IsConcurrencySafe() bool       { return true }
func (*VideoQueryTool) IsLongRunning() bool           { return true }
func (*VideoQueryTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }

func (t *VideoQueryTool) IsEnabled() bool {
	if t.source == nil || t.registry == nil {
		return false
	}
	ref := t.source.AgentVideoGeneration()
	if ref == "" {
		return false
	}
	ep, ok := t.source.ResolveEndpoint(ref)
	if !ok {
		return false
	}
	_, ok = t.registry.Get(ep.Provider)
	return ok
}

func (*VideoQueryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "Task ID returned by video_create.",
			},
			"timeout_s": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     maxQueryTimeoutS,
				"description": "Max seconds to block this call (default 3). Polls every 1s; after timeout does one final check, then returns status=running if still unfinished.",
			},
		},
		"required": []string{"task_id"},
	}
}

type queryInput struct {
	TaskID   string `json:"task_id"`
	TimeoutS int    `json:"timeout_s"`
}

func (t *VideoQueryTool) ValidateInput(raw json.RawMessage) error {
	var in queryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return errors.New("task_id is required")
	}
	if in.TimeoutS < 0 || in.TimeoutS > maxQueryTimeoutS {
		return fmt.Errorf("timeout_s must be between 1 and %d", maxQueryTimeoutS)
	}
	return nil
}

func (t *VideoQueryTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in queryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if err := t.ValidateInput(raw); err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if t.source == nil || t.registry == nil {
		return errResult("video_query is not configured", types.ToolErrorInternal), nil
	}
	ref := t.source.AgentVideoGeneration()
	ep, ok := t.source.ResolveEndpoint(ref)
	if !ok {
		return errResult("video_query: no usable video endpoint configured", types.ToolErrorInternal), nil
	}
	provider, ok := t.registry.Get(ep.Provider)
	if !ok {
		return errResult(fmt.Sprintf("video_query: provider %q not implemented", ep.Provider), types.ToolErrorInternal), nil
	}

	timeout := in.TimeoutS
	if timeout == 0 {
		timeout = defaultQueryTimeoutS
	}
	deadline := t.now().Add(time.Duration(timeout) * time.Second)
	req := QueryRequest{Endpoint: ep, TaskID: in.TaskID}

	// Blocking poll loop.
	for {
		done, toolRes := t.queryOnce(ctx, provider, req)
		if done {
			return toolRes, nil
		}
		if !t.now().Before(deadline) {
			break
		}
		t.sleep(pollInterval)
	}

	// final-check once after the deadline.
	done, toolRes := t.queryOnce(ctx, provider, req)
	if done {
		return toolRes, nil
	}
	return t.runningResult(in.TaskID), nil
}

// queryOnce performs one QueryTask + branch. done=true means we have a terminal
// ToolResult to return; done=false means queued/running (keep polling).
func (t *VideoQueryTool) queryOnce(ctx context.Context, provider VideoProvider, req QueryRequest) (bool, *types.ToolResult) {
	res, err := t.queryWithRetry(ctx, provider, req)
	if err != nil {
		return true, classifyProviderError("video query failed", err)
	}
	switch res.Status {
	case StatusSucceeded:
		return true, t.finishSuccess(ctx, provider, req.TaskID, res)
	case StatusQueued, StatusRunning:
		return false, nil
	case StatusFailed:
		return true, t.failedResult(req.TaskID, res)
	case StatusExpired, StatusCancelled, StatusNotFound:
		return true, t.terminalStatusResult(req.TaskID, res.Status)
	default:
		return true, errResult(fmt.Sprintf("video query: unknown status %q", res.Status), types.ToolErrorDependencyFail)
	}
}

// queryWithRetry retries transient provider errors with exp backoff (200/500/1000ms, max 3).
func (t *VideoQueryTool) queryWithRetry(ctx context.Context, provider VideoProvider, req QueryRequest) (*QueryResult, error) {
	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second}
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		res, err := provider.QueryTask(ctx, req)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrValidation) {
			return nil, err // non-retryable
		}
		if attempt < len(backoffs) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			t.sleep(backoffs[attempt])
		}
	}
	return nil, lastErr
}

func (t *VideoQueryTool) finishSuccess(ctx context.Context, provider VideoProvider, taskID string, res *QueryResult) *types.ToolResult {
	sessionRoot, err := resolveSessionRoot(ctx, t.rootDir)
	if err != nil {
		return errResult(err.Error(), types.ToolErrorInternal)
	}
	// 优先把视频落到当前 spawn 的 task_dir —— emma 调 promote 时按
	// {sessionRoot}/tasks/{task_id}/{basename} 找源文件，与之对齐。
	// 没有 TaskID 才 fallback 到 session-level generated/ 共享池。
	outDir := resolveOutDir(ctx, sessionRoot)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return errResult("create output directory: "+err.Error(), types.ToolErrorInternal)
	}

	data, _, err := t.downloadWithRetry(ctx, provider, res.VideoURL)
	if err != nil {
		// Download failed - still hand the original URL back as a fallback (24h valid).
		return &types.ToolResult{
			Content: "video generated but local download failed; use video_url_original (valid ~24h).",
			Metadata: map[string]any{
				"status":               "succeeded",
				"task_id":              taskID,
				"video_url_original":   res.VideoURL,
				"video_url_expires_at": res.URLExpiresAt.UTC().Format(time.RFC3339),
				"download_error":       err.Error(),
				"model":                res.Model,
				"resolution":           res.Resolution,
				"ratio":                res.Ratio,
				"duration":             res.Duration,
			},
		}
	}
	path := filepath.Join(outDir, "video-"+taskID+".mp4")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return errResult("write generated video: "+err.Error(), types.ToolErrorInternal)
	}
	emitDeliverable(ctx, path, len(data))

	return &types.ToolResult{
		Content: fmt.Sprintf("video ready: %s (%s, %s, %ds)", path, res.Model, res.Ratio, res.Duration),
		Metadata: map[string]any{
			"status":               "succeeded",
			"task_id":              taskID,
			"video_path":           path,
			"video_url_original":   res.VideoURL,
			"video_url_expires_at": res.URLExpiresAt.UTC().Format(time.RFC3339),
			"model":                res.Model,
			"resolution":           res.Resolution,
			"ratio":                res.Ratio,
			"duration":             res.Duration,
		},
	}
}

func (t *VideoQueryTool) downloadWithRetry(ctx context.Context, provider VideoProvider, url string) ([]byte, string, error) {
	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second}
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		data, mime, err := provider.DownloadVideo(ctx, url)
		if err == nil {
			return data, mime, nil
		}
		lastErr = err
		if attempt < len(backoffs) {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			t.sleep(backoffs[attempt])
		}
	}
	return nil, "", lastErr
}

func (t *VideoQueryTool) failedResult(taskID string, res *QueryResult) *types.ToolResult {
	return &types.ToolResult{
		Content: fmt.Sprintf("video task failed: %s (%s)", res.ErrorMessage, res.ErrorCode),
		Metadata: map[string]any{
			"status":        "failed",
			"task_id":       taskID,
			"error_code":    res.ErrorCode,
			"error_message": res.ErrorMessage,
			"hint":          "task failed; adjust the prompt and call video_create again to retry",
		},
	}
}

func (t *VideoQueryTool) terminalStatusResult(taskID string, status TaskStatus) *types.ToolResult {
	hint := map[TaskStatus]string{
		StatusExpired:   "task expired (7-day history limit); call video_create again to retry",
		StatusCancelled: "task was cancelled; call video_create again to retry",
		StatusNotFound:  "task not found (wrong id or past 7-day retention); call video_create again",
	}[status]
	return &types.ToolResult{
		Content: fmt.Sprintf("video task %s", status),
		Metadata: map[string]any{
			"status":  string(status),
			"task_id": taskID,
			"hint":    hint,
		},
	}
}

func (t *VideoQueryTool) runningResult(taskID string) *types.ToolResult {
	return &types.ToolResult{
		Content: "task still running",
		Metadata: map[string]any{
			"status":  "running",
			"task_id": taskID,
			"hint":    "task still running; call video_query again with the same task_id (任务仍在生成中，请稍后用同一 task_id 再调用 video_query)",
		},
	}
}
