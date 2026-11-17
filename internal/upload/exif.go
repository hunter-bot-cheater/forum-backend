package upload

import (
	"encoding/binary"
	"image"
)

// 本文件只做一件小事：把手机照片的 EXIF 方向摆正。
//
// 为什么必须做：相机与手机拍照时不会旋转像素，只在 EXIF 里写一个 Orientation，
// 由显示端负责转。我们重编码会把这个标签丢掉，于是「横着拍」的照片
// 存成头像后是躺着的，而用户再也没有信息把它转回来。
//
// 为什么自己解析而不是引一个 EXIF 库：只需要 IFD0 里的一个 SHORT，
// 四十来行就够，引一个通用库要处理 GPS、缩略图、十几种类型，都是没用到的部分。

// exifOrientationTag 是 Orientation 的标签号。
const exifOrientationTag = 0x0112

// jpegOrientation 从 JPEG 字节里读 EXIF 方向，读不到或格式不认识时返回 1（不旋转）。
//
// 只扫到 SOS（压缩数据开始）为止：APP1 段一定在压缩数据之前，
// 再往后走就要解析熵编码数据了，没有意义。
func jpegOrientation(data []byte) int {
	// SOI 是 FF D8，任何 JPEG 都以它开头。
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}

	i := 2
	for i+4 <= len(data) {
		// 段标记必须是 FF，不是的话说明这段已经错位，继续扫只会读出垃圾。
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		// FF 是允许出现在标记前的填充字节，跳过继续找。
		if marker == 0xFF {
			i++
			continue
		}
		// DA 是 SOS，后面就是压缩数据；D9 是 EOI，图已经结束。
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}

		size := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		// size 含它自己的两个字节，所以小于 2 不合法；
		// 超过剩余长度说明文件被截断，直接放弃。
		if size < 2 || i+2+size > len(data) {
			return 1
		}

		// E1 是 APP1，EXIF 就挂在这个段里。
		if marker == 0xE1 {
			if o := exifOrientation(data[i+4 : i+2+size]); o != 1 {
				return o
			}
		}
		i += 2 + size
	}
	return 1
}

// exifOrientation 解析 APP1 段的负载，返回 1~8 的方向值。
// 段落开头不是 Exif 标识（可能是 XMP 等别的 APP1）时返回 1。
func exifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[0:6]) != "Exif\x00\x00" {
		return 1
	}

	// 后面是 TIFF 结构：字节序标记 + 魔数 + IFD0 偏移。
	tiff := seg[6:]
	var order binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	// 42 是 TIFF 的固定魔数，对不上说明不是我们认识的格式。
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}

	// IFD 偏移是相对 TIFF 头部的，不是相对文件开头。
	off := int(order.Uint32(tiff[4:8]))
	if off < 0 || off+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[off : off+2]))
	// 每个条目固定 12 字节：tag(2) + type(2) + count(4) + value(4)。
	for k := 0; k < count; k++ {
		e := off + 2 + k*12
		if e+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[e:e+2]) != exifOrientationTag {
			continue
		}
		// SHORT 类型（type=3）的值就放在最后 4 个字节的前 2 个里，
		// 不按偏移去跳转——那是 LONG/ASCII 才需要的处理。
		value := int(order.Uint16(tiff[e+8 : e+10]))
		if value < 1 || value > 8 {
			return 1
		}
		return value
	}
	return 1
}

// applyOrientation 按 EXIF 方向把图像素摆正。
//
// 5~8 这四种会交换长宽（旋转 90 度），所以目标画布先按交换后的尺寸建，
// 再逐像素从源图取值。用显式循环而不是矩阵变换，是因为八种情况一条 switch
// 就能对着 EXIF 规范逐个核对，出错时一眼看得出是哪一行写反了。
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := orientation >= 5

	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}

	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 2: // 水平镜像
				sx, sy = w-1-x, y
			case 3: // 旋转 180 度
				sx, sy = w-1-x, h-1-y
			case 4: // 垂直镜像
				sx, sy = x, h-1-y
			case 5: // 沿主对角线翻转
				sx, sy = y, x
			case 6: // 顺时针 90 度
				sx, sy = y, h-1-x
			case 7: // 沿副对角线翻转
				sx, sy = w-1-y, h-1-x
			case 8: // 逆时针 90 度
				sx, sy = w-1-y, x
			default:
				sx, sy = x, y
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}
