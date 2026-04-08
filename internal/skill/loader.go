package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
	"harnessclaw-go/internal/command"
)

// Loader discovers and loads skills from configured directories.
// Directories are processed in order; earlier entries have higher priority
// on name conflicts (first-wins via seen map dedup).
type Loader struct {
	dirs   []string
	logger *zap.Logger
}

// NewLoader creates a skill loader that loads from the given directories.
// Each directory is scanned for SKILL.md (directory format) or *.md (flat format).
func NewLoader(dirs []string, logger *zap.Logger) *Loader {
	return &Loader{dirs: dirs, logger: logger}
}

// LoadAll loads skills from all configured directories and returns deduplicated commands.
func (l *Loader) LoadAll() ([]command.Command, error) {
	seen := make(map[string]bool) // realpath → already loaded
	var commands []command.Command
	var errs []string

	l.logger.Info("skill loader starting",
		zap.Int("dir_count", len(l.dirs)),
		zap.Strings("dirs", l.dirs),
	)

	for _, dir := range l.dirs {
		if dir == "" {
			continue
		}

		l.logger.Debug("scanning skill directory", zap.String("dir", dir))

		entries, err := discoverSkillEntries(dir)
		if err != nil {
			l.logger.Warn("skill directory not accessible",
				zap.String("dir", dir),
				zap.Error(err),
			)
			errs = append(errs, fmt.Sprintf("skip dir %s: %v", dir, err))
			continue
		}

		l.logger.Info("discovered skill entries",
			zap.String("dir", dir),
			zap.Int("entry_count", len(entries)),
			zap.Strings("entries", entries),
		)

		for _, entry := range entries {
			real, err := filepath.EvalSymlinks(entry)
			if err != nil {
				l.logger.Warn("failed to resolve symlink",
					zap.String("entry", entry),
					zap.Error(err),
				)
				errs = append(errs, fmt.Sprintf("resolve %s: %v", entry, err))
				continue
			}
			if seen[real] {
				l.logger.Debug("skill already loaded (dedup)",
					zap.String("entry", entry),
					zap.String("realpath", real),
				)
				continue
			}
			seen[real] = true

			cmd, err := LoadSkillFile(entry, command.SourceSkillDir, command.LoadedFromSkills)
			if err != nil {
				l.logger.Warn("failed to load skill file",
					zap.String("entry", entry),
					zap.Error(err),
				)
				errs = append(errs, fmt.Sprintf("load %s: %v", entry, err))
				continue
			}

			l.logger.Info("skill loaded",
				zap.String("file", entry),
				zap.String("name", cmd.GetName()),
				zap.String("type", string(cmd.Type)),
			)
			commands = append(commands, *cmd)
		}
	}

	l.logger.Info("skill loader finished",
		zap.Int("total_loaded", len(commands)),
		zap.Int("error_count", len(errs)),
	)

	var retErr error
	if len(errs) > 0 {
		retErr = fmt.Errorf("skill loading issues: %s", strings.Join(errs, "; "))
	}
	return commands, retErr
}

// discoverSkillEntries finds all skill files in a skills directory.
// Per TypeScript spec, skills directories only support directory format:
//   - skill-name/SKILL.md
//
// Single .md flat files are NOT supported in /skills/ directories
// (they were only supported in the deprecated /commands/ directories).
func discoverSkillEntries(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Directory format: skill-name/SKILL.md
		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillFile); err == nil {
			paths = append(paths, skillFile)
		}
	}

	sort.Strings(paths)
	return paths, nil
}

// LoadSkillFile reads a skill file and creates a Command from it.
func LoadSkillFile(path string, source command.CommandSource, loadedFrom command.LoadedFrom) (*command.Command, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill file %s: %w", path, err)
	}

	fm, body, err := ParseFrontmatter(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", path, err)
	}

	// Derive name from directory or filename if not in frontmatter.
	name := fm.Name
	if name == "" {
		if filepath.Base(path) == "SKILL.md" {
			// Directory format: /skills/my-skill/SKILL.md → "my-skill"
			name = filepath.Base(filepath.Dir(path))
		} else {
			// Flat format: /skills/my-skill.md → "my-skill"
			name = strings.TrimSuffix(filepath.Base(path), ".md")
		}
	}

	skillRoot := filepath.Dir(path)

	userInvocable := true
	if fm.UserInvocable != nil {
		userInvocable = *fm.UserInvocable
	}

	// Extract description from body if not in frontmatter.
	description := fm.Description
	if description == "" {
		description = extractDescriptionFromBody(body)
	}

	pc := &command.PromptCommand{
		CommandBase: command.CommandBase{
			Name:                   name,
			Description:            description,
			Aliases:                fm.Aliases,
			Source:                 source,
			LoadedFrom:             loadedFrom,
			IsEnabled:              true,
			WhenToUse:              fm.WhenToUse,
			Version:                fm.Version,
			UserInvocable:          userInvocable,
			DisableModelInvocation: fm.DisableModelInvocation,
			ArgumentHint:           fm.ArgumentHint,
		},
		AllowedTools:  fm.AllowedTools,
		Model:         fm.Model,
		Effort:        fm.Effort,
		Context:       fm.Context,
		Agent:         fm.Agent,
		ArgNames:      fm.Arguments,
		Paths:         fm.Paths,
		SkillRoot:     skillRoot,
		ContentLength: len(body),
		GetPromptForCommand: func(args string, ctx *command.PromptContext) ([]command.ContentBlock, error) {
			expanded := SubstituteArguments(body, args, true, fm.Arguments)

			// Replace built-in variables per TS spec:
			// ${CLAUDE_SKILL_DIR} → skill directory path (backslashes normalized)
			skillDir := strings.ReplaceAll(skillRoot, "\\", "/")
			expanded = strings.ReplaceAll(expanded, "${CLAUDE_SKILL_DIR}", skillDir)

			// ${CLAUDE_SESSION_ID} → current session ID
			if ctx != nil && ctx.SessionID != "" {
				expanded = strings.ReplaceAll(expanded, "${CLAUDE_SESSION_ID}", ctx.SessionID)
			}

			return []command.ContentBlock{
				{Type: "text", Text: expanded},
			}, nil
		},
	}

	return &command.Command{
		Type:   command.CommandTypePrompt,
		Prompt: pc,
	}, nil
}

// extractDescriptionFromBody extracts the first non-empty paragraph from the
// markdown body as a description fallback when frontmatter has no description.
// This mirrors the TypeScript behavior of auto-extracting description from content.
func extractDescriptionFromBody(body string) string {
	lines := strings.Split(body, "\n")
	var desc strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip headings and empty lines until we find a text paragraph.
		if trimmed == "" {
			if desc.Len() > 0 {
				break // end of first paragraph
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if desc.Len() > 0 {
				break
			}
			continue
		}
		if desc.Len() > 0 {
			desc.WriteString(" ")
		}
		desc.WriteString(trimmed)
	}
	result := desc.String()
	// Truncate long descriptions.
	if len(result) > 200 {
		result = result[:200] + "..."
	}
	return result
}
