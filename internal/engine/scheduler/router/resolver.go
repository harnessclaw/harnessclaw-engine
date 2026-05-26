package router

import "strings"

// AgentResolver picks which named agent executes a task step.
type AgentResolver interface {
	Resolve(goal string, available []string) string
}

type HeuristicAgentResolver struct{}

func NewHeuristicAgentResolver() *HeuristicAgentResolver { return &HeuristicAgentResolver{} }

func (r *HeuristicAgentResolver) Resolve(goal string, available []string) string {
	if len(available) == 0 {
		return ""
	}
	lower := strings.ToLower(goal)

	rules := []struct {
		name     string
		keywords []string
	}{
		{"researcher", []string{"research", "investigate", "调研", "搜索", "搜集", "survey", "find out"}},
		{"analyst", []string{"analyze", "analyse", "compare", "对比", "分析", "评估", "assess"}},
		{"writer", []string{"write", "draft", "report", "blog", "document", "写", "撰写", "起草", "文章"}},
		{"developer", []string{"implement", "code", "develop", "fix", "debug", "开发", "实现", "代码"}},
	}

	best, bestScore := "", 0
	for _, cand := range available {
		score := 0
		for _, rule := range rules {
			if rule.name == cand || strings.HasPrefix(cand, rule.name) {
				for _, kw := range rule.keywords {
					if strings.Contains(lower, kw) {
						score++
					}
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = cand
		}
	}
	if best != "" {
		return best
	}

	priority := []string{"freelancer", "general-purpose"}
	for _, p := range priority {
		for _, a := range available {
			if a == p {
				return a
			}
		}
	}
	return available[0]
}
