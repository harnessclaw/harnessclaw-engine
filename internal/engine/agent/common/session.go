package common

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"harnessclaw-go/internal/engine/session"
)

// BuildSubSession creates an ephemeral session for a sub-agent. The
// session ID convention is "<parentSessionID>_sub_<uuid8>" so logs
// and metrics can trace child-parent relationships.
//
// parentSessionID may be empty; in that case the ID is just
// "sub_<uuid8>" (used by orphan / smoke-test spawns).
func BuildSubSession(mgr *session.Manager, parentSessionID string) (*session.Session, error) {
	suffix := uuid.New().String()[:8]
	var id string
	if parentSessionID == "" {
		id = fmt.Sprintf("sub_%s", suffix)
	} else {
		id = fmt.Sprintf("%s_sub_%s", parentSessionID, suffix)
	}
	sess, err := mgr.GetOrCreate(context.Background(), id, "", "")
	if err != nil {
		return nil, fmt.Errorf("build sub-session: %w", err)
	}
	return sess, nil
}
