package sections

import (
	"harnessclaw-go/internal/engine/prompt"
)

// ArtifactsSection provides LLM guidance on using the artifact system
// to avoid re-generating content that is already stored.
type ArtifactsSection struct{}

func NewArtifactsSection() *ArtifactsSection {
	return &ArtifactsSection{}
}

func (s *ArtifactsSection) Name() string     { return "artifacts" }
func (s *ArtifactsSection) Priority() int    { return 21 } // right after tools(20)
func (s *ArtifactsSection) Cacheable() bool  { return true }
func (s *ArtifactsSection) MinTokens() int   { return 50 }

func (s *ArtifactsSection) Render(_ *prompt.PromptContext, _ int) (string, error) {
	return artifactsGuidance, nil
}

const artifactsGuidance = `# 产物缓存

当工具产出大量内容（文件内容、命令输出、生成的文本）时，系统会自动将其存储为产物，ID 格式如 ` + "`art_abc12345`" + `。
历史消息中，这些大结果可能显示为带产物 ID 的截断预览。

高效使用产物：
- 用 ArtifactGet 获取已存储产物的完整内容
- 用 Write 的 artifact_ref 参���将产物内容写入文件，而不是重新生成相同内容——这能节省大量输出 token
- 不要重新生成已存在的产物内容——用 ID 引用即可

示例：如果之前 Read 工具的结果存储��� art_abc12345，需要写到其他路径时：
  Write(file_path="/new/path.txt", artifact_ref="art_abc12345")
而不是在 content 参数中重新生成内容。`
