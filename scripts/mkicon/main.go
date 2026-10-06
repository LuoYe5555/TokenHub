// mkicon 把 jpg/png 图片转成 TokenHub 的内置图标：
//  1. internal/appicon/app.png   —— go:embed 内置标题栏/兜底图
//  2. internal/appicon/brand.png —— go:embed 品牌图（任务栏，可省略则沿用现有）
//  3. tokenhub.ico              —— exe 文件图标（用品牌图；需再跑 rsrc 重新生成 syso）
//
// 用法: go run ./scripts/mkicon <标题栏图片路径> [品牌图路径]
package main

import (
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"

	"tokenhub/internal/winicon"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: go run ./scripts/mkicon <标题栏图片路径> [品牌图路径]")
		os.Exit(1)
	}
	titleImg := decode(os.Args[1])
	writePNG("internal/appicon/app.png", titleImg)

	icoSrc := titleImg
	if len(os.Args) >= 3 {
		brandImg := decode(os.Args[2])
		writePNG("internal/appicon/brand.png", brandImg)
		icoSrc = brandImg
	}
	if err := winicon.WriteICOFile(filepath.Join(".", "tokenhub.ico"), icoSrc); err != nil {
		fmt.Fprintln(os.Stderr, "写 tokenhub.ico 失败:", err)
		os.Exit(1)
	}
	fmt.Println("已更新 tokenhub.ico")
	fmt.Println("下一步: 删除 rsrc_windows_amd64.syso 后用 rsrc 重新生成，再 go build")
}

func decode(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开图片失败:", err)
		os.Exit(1)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "解码图片失败:", err)
		os.Exit(1)
	}
	return img
}

func writePNG(rel string, img image.Image) {
	p, err := os.Create(filepath.Join(".", rel))
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建文件失败:", err)
		os.Exit(1)
	}
	if err := png.Encode(p, img); err != nil {
		p.Close()
		fmt.Fprintln(os.Stderr, "写文件失败:", err)
		os.Exit(1)
	}
	p.Close()
	fmt.Println("已更新", rel)
}
