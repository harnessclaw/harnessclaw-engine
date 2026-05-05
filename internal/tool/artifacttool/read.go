package artifacttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ReadToolName is the LLM-facing name. See WriteToolName for the
// PascalCase rationale.
const ReadToolName = "ArtifactRead"

// readInput is the parsed payload.
type readInput struct {
	ArtifactID string `json:"artifact_id"`
	// Mode selects the level of detail. Default 'preview' — that's the
	// "scan first, fetch full only if needed" behaviour doc §5 trains the
	// LLM into.
	Mode string `json:"mode,omitempty"`
}

// ReadTool fetches an artifact by ID at the requested detail level.
type ReadTool struct {
	tool.BaseTool
}

// NewReadTool returns the registered tool instance.
func NewReadTool() *ReadTool {
	return &ReadTool{}
}

func (*ReadTool) Name() string             { return ReadToolName }
func (*ReadTool) Description() string      { return readDescription }
func (*ReadTool) IsReadOnly() bool                  { return true }
func (*ReadTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (*ReadTool) IsEnabled() bool          { return true }
func (*ReadTool) IsConcurrencySafe() bool  { return true }

// InputSchema describes the LLM-facing arguments.
func (*ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"artifact_id": map[string]any{
				"type":        "string",
				"description": "ArtifactWrite 返回的 ID，格式 art_<hex>。",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"metadata", "preview", "full"},
				"description": "返回多少内容。'metadata' = 仅 name/description/preview；'preview' = 同 metadata（默认；preview 字段在两者中都包含）；'full' = 完整 content。",
			},
		},
		"required": []string{"artifact_id"},
	}
}

// ValidateInput rejects calls the store would reject anyway. ID format
// check filters out obvious hallucinations (doc §12).
//
// Hallucination diagnosis: when the format mismatch matches a UUID pattern
// (4 hex groups separated by dashes, total 32 hex + 4 dashes), we tell the
// LLM directly that it fabricated this — generic "invalid format" errors
// don't break the loop because the LLM thinks it just typo'd. The
// hallucination case warrants a STOP-AND-ESCALATE instruction, not a retry.
func (*ReadTool) ValidateInput(raw json.RawMessage) error {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if !artifact.IsValidID(in.ArtifactID) {
		return fmt.Errorf("%s", buildInvalidIDMessage(in.ArtifactID))
	}
	if in.Mode == "" {
		return nil
	}
	if !artifact.IsValidMode(artifact.ReadMode(in.Mode)) {
		return fmt.Errorf("mode must be metadata|preview|full, got %q", in.Mode)
	}
	return nil
}

// buildInvalidIDMessage crafts a context-aware rejection. The default
// case names the format; the UUID case calls out fabrication directly so
// the LLM doesn't just retry with another fake.
func buildInvalidIDMessage(id string) string {
	if looksLikeUUID(id) {
		return fmt.Sprintf(
			"artifact_id %q is a UUID — that is NOT how this system issues IDs. "+
				"Real IDs are exactly art_<24 hex chars>, no dashes. "+
				"You FABRICATED this id; do NOT retry with another guess. "+
				"Either copy the exact id from a recent ArtifactWrite tool result, "+
				"or call EscalateToPlanner to report the missing artifact reference.",
			id)
	}
	return fmt.Sprintf(
		"artifact_id %q has invalid format (expected art_<24 hex chars>). "+
			"IDs must be copied verbatim from an ArtifactWrite return value — "+
			"do not guess. If you don't have a real id, call EscalateToPlanner instead.",
		id)
}

// looksLikeUUID returns true for the canonical UUID hex pattern in either
// form: 8-4-4-4-12 with dashes (36 chars) or the same 32 hex compacted
// without dashes. Both are common LLM hallucination shapes — the model
// reaches for "what an ID looks like" from training data and our 24-hex
// shorter form isn't represented there.
func looksLikeUUID(id string) bool {
	s := id
	if len(s) > 4 && s[:4] == "art_" {
		s = s[4:]
	}
	switch len(s) {
	case 36: // dashed form
		for i, c := range s {
			isDashPos := i == 8 || i == 13 || i == 18 || i == 23
			if isDashPos {
				if c != '-' {
					return false
				}
				continue
			}
			if !isHex(c) {
				return false
			}
		}
		return true
	case 32: // compact form (no dashes)
		for _, c := range s {
			if !isHex(c) {
				return false
			}
		}
		return true
	}
	return false
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// Execute looks up the artifact and returns the requested detail level.
func (*ReadTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}

	store, ok := getStore(ctx)
	if !ok {
		return errResult("artifact store not configured for this engine"), nil
	}

	mode := artifact.ReadMode(in.Mode)
	if mode == "" {
		mode = artifact.ModePreview
	}

	a, err := store.Get(ctx, in.ArtifactID)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			return errResult(fmt.Sprintf("artifact %s not found (may have expired or never existed)", in.ArtifactID)), nil
		}
		if errors.Is(err, artifact.ErrAccessDenied) {
			return errResult(fmt.Sprintf("artifact %s is not readable in this scope", in.ArtifactID)), nil
		}
		return errResult("read artifact: " + err.Error()), nil
	}

	// Strip Content for the lighter modes so the LLM doesn't accidentally
	// see the full payload when it asked for "metadata".
	switch mode {
	case artifact.ModeMetadata, artifact.ModePreview:
		a.Content = ""
	}

	body, _ := json.Marshal(struct {
		Mode     artifact.ReadMode `json:"mode"`
		Artifact *artifact.Artifact `json:"artifact"`
	}{
		Mode:     mode,
		Artifact: a,
	})

	// render_hint = "artifact_view" — this is a READ, not a production.
	// Doc §10: only writes (render_hint=artifact) populate Engine event
	// Artifacts so the UI doesn't double-count a parent's artifact as if
	// the reader produced it. Two hints, two intents:
	//   - "artifact"      → producer wrote a new artifact (subagent_end aggregation)
	//   - "artifact_view" → consumer fetched an existing one (no aggregation)
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint": "artifact_view",
			"artifact_id": a.ID,
			"mode":        string(mode),
			"name":        a.Name,
			"type":        string(a.Type),
		},
	}, nil
}

const readDescription = `按 ID 取一份存好的 artifact。

⚠️ 调用前红线 ⚠️
- artifact_id **只能复制**：来自 ArtifactWrite 的返回值，或上游消息里明确给出的 ID。
- **不允许猜、不允许编、不允许用 UUID 习惯（带 "-" 的）拼**。
- 真实 ID = ` + "`art_`" + ` + 正好 24 个 16 进制字符（如 ` + "`art_2a7f0e8b4c9d11ef0a36e613`" + `）。
- 不确定 ID 时，**不要调本工具**——sub-agent 用 EscalateToPlanner 上报，L2 在 summary 里说明缺失。

三种 mode（用能解决你问题的最轻一档）：
- metadata：name + description + preview + size。用来判断"我到底需不需要这份 artifact"。
- preview：同 metadata（preview 字段在两者中都有）。**默认值**。
- full：完整 content。只在确实需要按字节处理时才用——比如要自己做汇总/转换。

读不存在或已过期的 artifact 会返回错误；把它当成可恢复错误处理（重规划或追问）。`
