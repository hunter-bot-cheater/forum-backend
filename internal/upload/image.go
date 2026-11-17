package upload

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
)

// alphaKeepRatio 是「透明像素占比达到多少就必须留 PNG」的阈值（百分比）。
//
// 1% 这个数是从实际图片来的：手机截图、相机导出、修图软件另存的 PNG
// 经常在四角残留几十个半透明像素，整张图看起来完全不透明。
// 这种图如果按 PNG 存，体积是 JPEG 的三四倍，为几个看不见的边缘多付几倍流量。
// 真正需要透明的图（抠图头像、logo）透明面积远超 1%，不会误判。
const alphaKeepRatio = 1

// fitWithin 把图缩到最长边不超过 max，保持长宽比。
//
// 已经够小的图原样返回，不重采样：多一次缩放只会让图更糊，省不下几个字节。
func fitWithin(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}

	// 按最长边算比例，短边跟着一起缩，否则图会被拉变形。
	longest := w
	if h > w {
		longest = h
	}
	scale := float64(max) / float64(longest)
	nw := int(math.Round(float64(w) * scale))
	nh := int(math.Round(float64(h) * scale))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	// NRGBA 保留透明通道；用 CatmullRom 而不是最近邻，
	// 缩小三四倍时最近邻会丢细节、出现锯齿，头像上的人脸会变得很脏。
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// alphaStats 统计透明像素，返回「有没有任何透明像素」与「透明像素是否足够多」。
//
// 分两个问题是因为它们对应两种决策：
//   - 有没有：决定要不要在出 JPEG 之前先铺白底，否则透明区域会变成黑色；
//   - 够不够多：决定输出 PNG（保留透明）还是 JPEG（省体积）。
func alphaStats(img image.Image) (hasAny bool, keepAlpha bool) {
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if total == 0 {
		return false, false
	}

	transparent := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a < 0xffff {
				transparent++
			}
		}
	}
	if transparent == 0 {
		return false, false
	}
	return true, transparent*100 >= total*alphaKeepRatio
}

// flattenOnWhite 把图合成到白底上，丢掉透明通道。
//
// 用白色而不是黑色：站点卡片是白底，半透明边缘合到白底看不出接缝；
// 合到黑底会在头像周围留一圈脏边。
func flattenOnWhite(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

// encodeJPEG 重编码为 JPEG。质量由配置决定，4:2:0 色度抽样是 Go 的默认行为，
// 对头像这种小图完全够用，而且比 4:4:4 再省一截。
func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("upload: encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// encodePNG 重编码为 PNG。
// 用 BestCompression：输出图最多 256 px，多花的几毫秒换来的体积下降是划算的，
// 而且这张图只编码一次、要被下载无数次。
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("upload: encode png: %w", err)
	}
	return buf.Bytes(), nil
}
