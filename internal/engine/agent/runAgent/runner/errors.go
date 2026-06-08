package runner

import "errors"

// errNilCfg is returned when RunLeaf is invoked with Input.Cfg == nil.
// This is a programming error at the caller — every spawn must carry a
// SpawnConfig.
var errNilCfg = errors.New("runner: Input.Cfg is required")
