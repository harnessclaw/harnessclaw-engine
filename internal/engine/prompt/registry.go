package prompt

import "sort"

// Registry manages the global pool of available sections.
type Registry struct {
	sections map[string]Section
}

// NewRegistry creates an empty section registry.
func NewRegistry() *Registry {
	return &Registry{
		sections: make(map[string]Section),
	}
}

// Register adds a section to the registry.
// If a section with the same name already exists, it is replaced.
func (r *Registry) Register(s Section) {
	r.sections[s.Name()] = s
}

// Get retrieves a section by name.
func (r *Registry) Get(name string) (Section, bool) {
	s, ok := r.sections[name]
	return s, ok
}

// GetAll returns all registered sections.
func (r *Registry) GetAll() []Section {
	sections := make([]Section, 0, len(r.sections))
	for _, s := range r.sections {
		sections = append(sections, s)
	}
	return sections
}

// GetFiltered returns sections filtered and sorted according to a profile.
// If profile is nil, returns all sections sorted by priority.
func (r *Registry) GetFiltered(profile *AgentProfile) []Section {
	var sections []Section

	if profile == nil || len(profile.Sections) == 0 {
		// No profile or empty sections list = include all
		sections = r.GetAll()
	} else {
		// Include only sections listed in profile
		for _, name := range profile.Sections {
			if s, ok := r.sections[name]; ok {
				sections = append(sections, s)
			}
		}
	}

	// Apply exclusions
	if profile != nil && len(profile.ExcludeSections) > 0 {
		excluded := make(map[string]bool)
		for _, name := range profile.ExcludeSections {
			excluded[name] = true
		}

		filtered := make([]Section, 0, len(sections))
		for _, s := range sections {
			if !excluded[s.Name()] {
				filtered = append(filtered, s)
			}
		}
		sections = filtered
	}

	// Sort: cacheable first, then by priority within each group
	sort.SliceStable(sections, func(i, j int) bool {
		ci, cj := sections[i].Cacheable(), sections[j].Cacheable()
		if ci != cj {
			return ci // cacheable before non-cacheable
		}
		return sections[i].Priority() < sections[j].Priority()
	})

	return sections
}
