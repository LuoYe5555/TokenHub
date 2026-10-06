// Package appicon 内置应用图标：
//   - app.png   标题栏/兜底图（用户自定义，如狗图；可用 scripts/mkicon 重新生成）
//   - brand.png TokenHub "T" 品牌图（任务栏 + exe 文件图标固定用）
package appicon

import (
	"bytes"
	"image"
	"image/png"

	_ "embed"
)

//go:embed app.png
var appPNG []byte

//go:embed brand.png
var brandPNG []byte

// Image 解码内置标题栏/兜底图。
func Image() (image.Image, error) {
	return png.Decode(bytes.NewReader(appPNG))
}

// BrandImage 解码内置品牌 "T" 图（任务栏/exe 图标）。
func BrandImage() (image.Image, error) {
	return png.Decode(bytes.NewReader(brandPNG))
}
