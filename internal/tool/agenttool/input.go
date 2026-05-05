package agenttool

import (
	"encoding/json"
	"fmt"

	"harnessclaw-go/pkg/types"
)

// agentInput is the parsed input for the Agent tool.
type agentInput struct {
	Prompt          string                 `json:"prompt"`
	SubagentType    string                 `json:"subagent_type"`
	Description     string                 `json:"description"`
	Name            string                 `json:"name"`
	Model           string                 `json:"model"`
	Fork            bool                   `json:"fork"`
	RunInBackground bool                   `json:"run_in_background"`
	// ExpectedOutputs is the deliverable contract — see doc §3 (mechanisms
	// M3/M4) and pkg/types.ExpectedOutput. The dispatching agent (e.g.
	// Specialists) declares what artifacts the L3 must submit; the
	// framework enforces it via SubmitTaskResult.
	ExpectedOutputs []types.ExpectedOutput `json:"expected_outputs,omitempty"`
}

// parseInput unmarshals raw JSON into agentInput.
func parseInput(raw json.RawMessage) (*agentInput, error) {
	var input agentInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid Agent tool input: %w", err)
	}
	return &input, nil
}

// validate checks that the input has all required fields and valid values.
func (i *agentInput) validate() error {
	if i.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}

	// Validate subagent_type if provided.
	if i.SubagentType != "" {
		switch i.SubagentType {
		case "general-purpose", "Explore", "explore", "Plan", "plan":
			// valid
		default:
			return fmt.Errorf("unsupported subagent_type: %s (valid: general-purpose, Explore, Plan)", i.SubagentType)
		}
	}

	return nil
}

// inputSchema is the JSON Schema for the Agent tool's input.
var inputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"prompt": map[string]any{
			"type":        "string",
			"description": "派给 sub-agent 的任务指令。",
		},
		"subagent_type": map[string]any{
			"type":        "string",
			"description": "要 spawn 的 sub-agent 类型。决定工具集和 prompt profile。",
			"enum":        []string{"general-purpose", "Explore", "Plan"},
		},
		"description": map[string]any{
			"type":        "string",
			"description": "3-5 词的简短任务描述。",
		},
		"name": map[string]any{
			"type":        "string",
			"description": "可选 sub-agent 名字，用于日志中标识。",
		},
		"model": map[string]any{
			"type":        "string",
			"description": "可选 model 覆盖。为空时继承父 agent 的 model。",
		},
		"fork": map[string]any{
			"type":        "boolean",
			"description": "为 true 时 sub-agent 继承父 agent 的对话上下文。",
		},
		"run_in_background": map[string]any{
			"type":        "boolean",
			"description": "为 true 时异步运行，立刻返回 agent ID。",
		},
		"expected_outputs": map[string]any{
			"type":        "array",
			"description": "可选的「必交产物」契约。每一项声明一份 sub-agent 必须用 SubmitTaskResult 提交的 artifact。任务有结构化产出（报告/表格/数据文件）时使用；零散任务可省。框架会服务端校验 type / size / role 是否匹配，挡住「声称完成实际没产出」的失败。",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"role": map[string]any{
						"type":        "string",
						"description": "契约标识，sub-agent 提交时按这个回报。示例：'comparison_table'、'findings_report'。",
					},
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"structured", "file", "blob"},
						"description": "artifact 类型（structured = 带 schema 的 JSON 数据；file = 文本；blob = 二进制）。",
					},
					"mime_type": map[string]any{
						"type":        "string",
						"description": "可选 MIME 约束，例如 'text/csv'、'application/json'。",
					},
					"schema": map[string]any{
						"type":        "object",
						"description": "可选 JSON 描述结构化形态（列、字段类型等）。",
					},
					"min_size_bytes": map[string]any{
						"type":        "integer",
						"description": "最小内容字节数——挡占位 / 空提交。默认 1。",
					},
					"required": map[string]any{
						"type":        "boolean",
						"description": "为 true 表示必交；false 为可选。",
					},
					"acceptance_criteria": map[string]any{
						"type":        "string",
						"description": "自由文本质量标准（未来用于 LLM-as-judge 评分，目前仅信息记录）。",
					},
				},
				"required": []string{"role"},
			},
		},
	},
	"required": []string{"prompt"},
}
