// Package winicon 把任意图片转成 Windows 窗口图标（标题栏 + 任务栏）。
package winicon

import (
	"bytes"
	"image"
	"image/png"
	"os"
)

// EncodeICO 把图片编码为 ICO 文件字节（Vista+ 支持 PNG 压缩条目）。
func EncodeICO(img image.Image) ([]byte, error) {
	scaled := boxScale(img, 256, 256)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, scaled); err != nil {
		return nil, err
	}
	pngBytes := pngBuf.Bytes()
	// ICONDIR(6) + ICONDIRENTRY(16) + PNG
	out := make([]byte, 22)
	out[0] = 0; out[1] = 0 // reserved
	out[2] = 1; out[3] = 0 // type: icon
	out[4] = 1; out[5] = 0 // count: 1
	e := out[6:]
	e[0] = 0                     // width (0 = 256)
	e[1] = 0                     // height (0 = 256)
	e[2] = 0                     // color count
	e[3] = 0                     // reserved
	e[4] = 1; e[5] = 0           // planes
	e[6] = 32; e[7] = 0          // bit count
	putU32(e[8:], uint32(len(pngBytes)))
	putU32(e[12:], 22)
	return append(out, pngBytes...), nil
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v); b[1] = byte(v >> 8); b[2] = byte(v >> 16); b[3] = byte(v >> 24)
}

// boxScale 区域平均缩放（缩小照片质量优于最近邻）。
func boxScale(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy0 := y * sh / h
		sy1 := (y + 1) * sh / h
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < w; x++ {
			sx0 := x * sw / w
			sx1 := (x + 1) * sw / w
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					c := src.At(b.Min.X+sx, b.Min.Y+sy)
					cr, cg, cb, ca := c.RGBA()
					r += uint64(cr); g += uint64(cg); bl += uint64(cb); a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			// RGBA() 返回 16bit 预乘值，转回 8bit 非预乘
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8((r / n) >> 8)
			dst.Pix[i+1] = uint8((g / n) >> 8)
			dst.Pix[i+2] = uint8((bl / n) >> 8)
			dst.Pix[i+3] = uint8((a / n) >> 8)
		}
	}
	return dst
}

// WriteICOFile 把图标写到文件（每次启动重新生成，成本低）。
func WriteICOFile(path string, img image.Image) error {
	data, err := EncodeICO(img)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
