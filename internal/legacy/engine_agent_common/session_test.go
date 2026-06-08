package common_test

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/memory"
)

func TestBuildSubSession_IDConvention(t *testing.T) {
	mgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)

	sess, err := common.BuildSubSession(mgr, "parent_xyz")
	if err != nil {
		t.Fatalf("BuildSubSession: %v", err)
	}
	if !strings.HasPrefix(sess.ID, "parent_xyz_sub_") {
		t.Errorf("sess.ID = %q, want prefix parent_xyz_sub_", sess.ID)
	}
	if len(sess.ID) != len("parent_xyz_sub_")+8 {
		t.Errorf("sub-session id should have 8-char uuid suffix; got %q", sess.ID)
	}
}

func TestBuildSubSession_EmptyParent(t *testing.T) {
	mgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)

	sess, err := common.BuildSubSession(mgr, "")
	if err != nil {
		t.Fatalf("BuildSubSession with empty parent: %v", err)
	}
	if !strings.HasPrefix(sess.ID, "sub_") {
		t.Errorf("sess.ID = %q, want prefix sub_", sess.ID)
	}
}
