package types

import "testing"

func TestIDTypesAreDistinct(t *testing.T) {
	var a AgentID = "a-1"
	var b TaskID = "t-1"
	var c SessionID = "s-1"
	// 编译期保证不同类型不能互相赋值
	_ = a; _ = b; _ = c
}
