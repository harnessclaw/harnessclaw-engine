package artifact

import "harnessclaw-go/internal/tool"

// storeAdapter wraps Store to satisfy tool.ArtifactStore.
type storeAdapter struct {
	s *Store
}

// AsToolStore returns a tool.ArtifactStore backed by the given Store.
func AsToolStore(s *Store) tool.ArtifactStore {
	return &storeAdapter{s: s}
}

func (a *storeAdapter) Get(id string) tool.ArtifactContent {
	art := a.s.Get(id)
	if art == nil {
		return tool.ArtifactContent{}
	}
	return tool.ArtifactContent{
		ID:      art.ID,
		Content: art.Content,
		Size:    art.Size,
	}
}
