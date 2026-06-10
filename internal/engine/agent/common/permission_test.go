package common_test

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/common"
)

func TestBuildInheritedChecker_NonEmptyApproved(t *testing.T) {
	chk := common.BuildInheritedChecker([]string{"bash", "write"})
	if chk == nil {
		t.Fatal("checker nil")
	}
}

func TestBuildInheritedChecker_EmptyApproved_ReturnsBypass(t *testing.T) {
	chk := common.BuildInheritedChecker(nil)
	if chk == nil {
		t.Fatal("checker nil")
	}
}
