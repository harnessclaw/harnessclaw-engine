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

const artifactsGuidance = `# Artifacts

When a tool produces substantial output (file contents, command output, generated text),
it is automatically stored as an artifact with an ID like ` + "`art_abc12345`" + `.
In earlier messages, these large results may appear as truncated previews with their artifact ID.

To work efficiently with artifacts:
- Use ArtifactGet to retrieve the full content of a stored artifact when you need it.
- Use Write with the artifact_ref parameter to write artifact content to a file,
  instead of regenerating the same content inline. This saves significant output tokens.
- Do NOT regenerate content that already exists as an artifact — reference it by ID instead.

Example: if a previous Read tool result was stored as art_abc12345, and you need to
write that content to a different path, use:
  Write(file_path="/new/path.txt", artifact_ref="art_abc12345")
instead of regenerating the content in the content parameter.`
