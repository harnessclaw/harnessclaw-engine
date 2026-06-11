package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// SkillCard is the metadata view of a skill, used by SearchSkill output.
// Body is intentionally absent — LoadSkill fetches it via Reader.Load.
// Path is internal: SearchSkill outputs `json:"-"` to avoid leaking absolute
// paths to the LLM; LoadSkill injects skill root via a separate XML attr.
type SkillCard struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	WhenToUse    string   `json:"when_to_use,omitempty"`
	Version      string   `json:"version,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Path         string   `json:"-"`
}

// SkillFull carries the SKILL.md body in addition to metadata.
// Returned by Reader.Load.
type SkillFull struct {
	SkillCard
	Body string `json:"body"`
}

// Reader scans configured skill directories on demand. Independent of
// Loader.LoadAll (which runs at server start) — Reader is for runtime
// discovery while L2/L3 are working.
type Reader struct {
	dirs   []string
	mu     sync.Mutex
	cache  *readerCache
	logger *zap.Logger
}

type readerCache struct {
	cards     []SkillCard
	fetchedAt time.Time
}

const readerCacheTTL = 5 * time.Second

// NewReader constructs a Reader. Pass cfg.Skills.Dirs.
func NewReader(dirs []string, logger *zap.Logger) *Reader {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Reader{dirs: dirs, logger: logger}
}

// Search returns metadata-only SkillCards across all configured dirs.
// query, if non-empty, filters by case-insensitive substring match on
// Name / Description / WhenToUse. limit caps results (0 → 20, max 50).
func (r *Reader) Search(query string, limit int) ([]SkillCard, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	cards, err := r.scan()
	if err != nil {
		return nil, err
	}

	if query == "" {
		if len(cards) > limit {
			cards = cards[:limit]
		}
		return cards, nil
	}

	// Token-based match: LLMs naturally write multi-word queries like
	// "docx doc word" — the previous whole-string `strings.Contains`
	// would zero-hit those because no skill's metadata literally
	// contains the joined string.
	//
	// Semantics: each whitespace-delimited token is matched independently
	// against the skill's haystack (name + description + when_to_use,
	// case-insensitive). A skill is a hit when ANY token appears in the
	// haystack — OR matching, biased for recall. Skills are then ranked
	// by (a) match count desc, (b) whether the token hit the name (a
	// strong intent signal), (c) alphabetical for stable ordering.
	//
	// Tradeoff vs strict AND: AND would feel "smarter" for short tags
	// but produces zero-hit dead-ends from LLM phrasing variability;
	// users / planners get more value from "found something close" +
	// rank than from "empty list, try again".
	tokens := tokenizeQuery(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	type scored struct {
		card     SkillCard
		hits     int
		nameHit  bool
	}
	var ranked []scored
	for _, c := range cards {
		name := strings.ToLower(c.Name)
		hay := strings.ToLower(c.Name + " " + c.Description + " " + c.WhenToUse)
		hits := 0
		nameHit := false
		for _, t := range tokens {
			if strings.Contains(hay, t) {
				hits++
				if strings.Contains(name, t) {
					nameHit = true
				}
			}
		}
		if hits > 0 {
			ranked = append(ranked, scored{card: c, hits: hits, nameHit: nameHit})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].hits != ranked[j].hits {
			return ranked[i].hits > ranked[j].hits
		}
		if ranked[i].nameHit != ranked[j].nameHit {
			return ranked[i].nameHit
		}
		return ranked[i].card.Name < ranked[j].card.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]SkillCard, 0, len(ranked))
	for _, s := range ranked {
		out = append(out, s.card)
	}
	return out, nil
}

// tokenizeQuery splits a query string into lowercased tokens. Whitespace,
// commas, and the common Chinese fullwidth comma are all treated as
// separators so LLM-written queries like "docx, word" or "docx 文档"
// fan out properly.
func tokenizeQuery(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	// Separators: ASCII whitespace + comma + semicolon, plus the
	// Chinese fullwidth comma (U+FF0C) and fullwidth semicolon
	// (U+FF1B) which LLMs frequently emit in zh queries.
	fields := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', ';', '，', '；':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// scan walks all configured dirs and returns deduplicated SkillCards.
// Caches the result for readerCacheTTL.
func (r *Reader) scan() ([]SkillCard, error) {
	r.mu.Lock()
	if r.cache != nil && time.Since(r.cache.fetchedAt) < readerCacheTTL {
		c := r.cache.cards
		r.mu.Unlock()
		return c, nil
	}
	r.mu.Unlock()

	seen := make(map[string]bool)
	var cards []SkillCard
	for _, dir := range r.dirs {
		if dir == "" {
			continue
		}
		paths, err := discoverSkillEntries(dir)
		if err != nil {
			r.logger.Warn("skill reader: dir not accessible",
				zap.String("dir", dir), zap.Error(err))
			continue
		}
		for _, p := range paths {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				r.logger.Warn("skill reader: symlink resolve failed",
					zap.String("entry", p), zap.Error(err))
				continue
			}
			if seen[real] {
				continue
			}
			seen[real] = true

			content, err := os.ReadFile(p)
			if err != nil {
				r.logger.Warn("skill reader: read failed",
					zap.String("path", p), zap.Error(err))
				continue
			}
			fm, _, err := ParseFrontmatter(string(content))
			if err != nil {
				r.logger.Warn("skill reader: frontmatter parse failed",
					zap.String("path", p), zap.Error(err))
				continue
			}
			name := fm.Name
			if name == "" {
				name = filepath.Base(filepath.Dir(p))
			}
			cards = append(cards, SkillCard{
				Name:         name,
				Description:  fm.Description,
				WhenToUse:    fm.WhenToUse,
				Version:      fm.Version,
				AllowedTools: []string(fm.AllowedTools),
				Path:         filepath.Dir(p), // skill root, NOT SKILL.md
			})
		}
	}

	sort.Slice(cards, func(i, j int) bool { return cards[i].Name < cards[j].Name })

	r.mu.Lock()
	r.cache = &readerCache{cards: cards, fetchedAt: time.Now()}
	r.mu.Unlock()
	return cards, nil
}

// Load returns the full SkillFull including body. body is not cached
// to avoid memory growth (skill body can be 100KB-ish per the spec cap).
func (r *Reader) Load(name string) (*SkillFull, error) {
	cards, err := r.scan()
	if err != nil {
		return nil, err
	}
	var card *SkillCard
	for i := range cards {
		if cards[i].Name == name {
			card = &cards[i]
			break
		}
	}
	if card == nil {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	skillMd := filepath.Join(card.Path, "SKILL.md")
	content, err := os.ReadFile(skillMd)
	if err != nil {
		return nil, fmt.Errorf("read skill body %s: %w", skillMd, err)
	}
	_, body, err := ParseFrontmatter(string(content))
	if err != nil {
		return nil, fmt.Errorf("frontmatter parse %s: %w", skillMd, err)
	}
	return &SkillFull{SkillCard: *card, Body: body}, nil
}
