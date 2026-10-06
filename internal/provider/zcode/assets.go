package zcode

import _ "embed"

// shapeJSON ZCode 客户端 plan 请求形状模板（捕获自真实客户端流量）。
//
//go:embed zcodePlanShape.json
var shapeJSON []byte
