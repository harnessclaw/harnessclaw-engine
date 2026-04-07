package websocket

import "sync"

// ConnRegistry maps session IDs to their active WebSocket connections.
// A single session may have multiple connections (viewer mode).
type ConnRegistry struct {
	mu    sync.RWMutex
	conns map[string]map[string]*Conn // sessionID → connID → *Conn
}

// NewConnRegistry creates an empty registry.
func NewConnRegistry() *ConnRegistry {
	return &ConnRegistry{
		conns: make(map[string]map[string]*Conn),
	}
}

// Register adds a connection to the registry.
func (r *ConnRegistry) Register(c *Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.conns[c.sessionID]
	if !ok {
		m = make(map[string]*Conn)
		r.conns[c.sessionID] = m
	}
	m[c.id] = c
}

// Unregister removes a connection from the registry.
func (r *ConnRegistry) Unregister(sessionID, connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.conns[sessionID]; ok {
		delete(m, connID)
		if len(m) == 0 {
			delete(r.conns, sessionID)
		}
	}
}

// GetBySession returns all connections for a session. Callers must not modify
// the returned slice.
func (r *ConnRegistry) GetBySession(sessionID string) []*Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.conns[sessionID]
	if !ok {
		return nil
	}
	out := make([]*Conn, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	return out
}

// All returns every active connection across all sessions.
func (r *ConnRegistry) All() []*Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Conn
	for _, m := range r.conns {
		for _, c := range m {
			out = append(out, c)
		}
	}
	return out
}

// CloseAll sends a close frame to every connection and removes them.
func (r *ConnRegistry) CloseAll() {
	r.mu.Lock()
	all := make([]*Conn, 0)
	for _, m := range r.conns {
		for _, c := range m {
			all = append(all, c)
		}
	}
	r.conns = make(map[string]map[string]*Conn)
	r.mu.Unlock()

	for _, c := range all {
		c.Close()
	}
}
