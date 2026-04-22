package prompt

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Builder assembles system prompts from sections.
type Builder struct {
	registry  *Registry
	allocator *BudgetAllocator
	logger    *zap.Logger
}

// NewBuilder creates a prompt builder.
func NewBuilder(registry *Registry, logger *zap.Logger) *Builder {
	return &Builder{
		registry:  registry,
		allocator: NewBudgetAllocator(),
		logger:    logger,
	}
}

// Build assembles a system prompt from sections according to the profile.
func (b *Builder) Build(ctx *PromptContext, profile *AgentProfile) (*PromptOutput, error) {
	startTime := time.Now()

	// Get filtered and sorted sections
	sections := b.registry.GetFiltered(profile)
	if len(sections) == 0 {
		return &PromptOutput{
			Version: "p_empty",
			Metadata: PromptMetadata{
				ProfileName:   getProfileName(profile),
				BuildDuration: time.Since(startTime),
			},
		}, nil
	}

	// Compute token budget
	budget := ComputeSystemPromptBudget(
		ctx.ContextWindowSize,
		ctx.TotalTokensUsed,
		16384, // maxOutputTokens - could be from config
		DefaultSafetyMargin,
	)

	// Override budget if profile specifies
	if profile != nil && profile.TokenBudget > 0 {
		budget = profile.TokenBudget
	}

	// Allocate budget across sections
	allocation := b.allocator.Allocate(sections, budget)

	// Render sections
	var blocks []PromptBlock
	var skipped []SkipRecord
	sectionTokens := make(map[string][2]int)

	for _, s := range sections {
		allocated := allocation[s.Name()]

		b.logger.Debug("section budget",
			zap.String("section", s.Name()),
			zap.Int("allocated", allocated),
			zap.Int("min_tokens", s.MinTokens()),
			zap.Bool("cacheable", s.Cacheable()),
			zap.Int("priority", s.Priority()),
		)

		// Check if section has override in profile
		if profile != nil && profile.SectionOverrides != nil {
			if override, ok := profile.SectionOverrides[s.Name()]; ok {
				tokens := EstimateTokens(override)
				blocks = append(blocks, PromptBlock{
					Name:            s.Name(),
					Content:         override,
					Cacheable:       s.Cacheable(),
					EstimatedTokens: tokens,
				})
				sectionTokens[s.Name()] = [2]int{allocated, tokens}
				continue
			}
		}

		// Skip if below MinTokens
		if allocated < s.MinTokens() {
			skipped = append(skipped, SkipRecord{
				Section: s.Name(),
				Reason:  fmt.Sprintf("budget (%d < %d)", allocated, s.MinTokens()),
			})
			continue
		}

		// Render section
		content, err := s.Render(ctx, allocated)
		if err != nil {
			b.logger.Warn("section render failed",
				zap.String("section", s.Name()),
				zap.Error(err),
			)
			skipped = append(skipped, SkipRecord{
				Section: s.Name(),
				Reason:  fmt.Sprintf("error: %v", err),
			})
			continue
		}

		// Skip if empty
		if content == "" {
			skipped = append(skipped, SkipRecord{
				Section: s.Name(),
				Reason:  "empty",
			})
			continue
		}

		tokens := EstimateTokens(content)
		blocks = append(blocks, PromptBlock{
			Name:            s.Name(),
			Content:         content,
			Cacheable:       s.Cacheable(),
			EstimatedTokens: tokens,
		})
		sectionTokens[s.Name()] = [2]int{allocated, tokens}
	}

	// Compute metrics
	totalTokens := 0
	cacheableTokens := 0
	cacheableCount := 0
	for _, b := range blocks {
		totalTokens += b.EstimatedTokens
		if b.Cacheable {
			cacheableTokens += b.EstimatedTokens
			cacheableCount++
		}
	}

	cacheableRatio := 0.0
	if totalTokens > 0 {
		cacheableRatio = float64(cacheableTokens) / float64(totalTokens)
	}

	output := &PromptOutput{
		Blocks:  blocks,
		Version: ComputeVersion(blocks),
		Metadata: PromptMetadata{
			ProfileName:     getProfileName(profile),
			TotalTokens:     totalTokens,
			TokenBudget:     budget,
			SkippedSections: skipped,
			SectionTokens:   sectionTokens,
			CacheMetrics: CacheMetrics{
				CacheableTokens:     cacheableTokens,
				DynamicTokens:       totalTokens - cacheableTokens,
				CacheableRatio:      cacheableRatio,
				CacheableBlockCount: cacheableCount,
			},
			BuildDuration: time.Since(startTime),
		},
	}

	// Log build summary
	b.logger.Info("prompt built",
		zap.String("version", output.Version),
		zap.String("profile", output.Metadata.ProfileName),
		zap.Int("total_tokens", totalTokens),
		zap.Int("budget", budget),
		zap.Float64("cacheable_ratio", cacheableRatio),
		zap.Int("block_count", len(blocks)),
		zap.Int("skipped_count", len(skipped)),
		zap.Duration("build_duration", output.Metadata.BuildDuration),
	)

	// Warn if cacheable ratio is low
	if cacheableRatio < 0.3 && totalTokens > 0 {
		b.logger.Warn("low prompt cache potential",
			zap.Float64("cacheable_ratio", cacheableRatio),
			zap.String("hint", "consider marking more sections as cacheable"),
		)
	}

	return output, nil
}

func getProfileName(profile *AgentProfile) string {
	if profile == nil {
		return "default"
	}
	return profile.Name
}
