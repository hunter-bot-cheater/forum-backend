package upload

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这些用例不连库也不起 HTTP：上传模块只碰内存和本地目录，
// 用 t.TempDir() 就够，跑完自动清理。

// newService 建一个落在临时目录的服务，并把「现在」固定住，
// 这样断言路径里的年月不会随运行时间变形。
func newService(t *testing.T, cfg Config) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfg.Dir = dir
	if cfg.URLPrefix == "" {
		cfg.URLPrefix = "/uploads"
	}
	s, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s.now = func() time.Time {
		return time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)
	}
	return s, dir
}

// noiseImage 生成一张满是随机像素的图。
// 用随机色而不是纯色：纯色图重编码后只有几百字节，
// 「压缩后更小」的断言就会变成一句废话，测不出真的有没有压。
func noiseImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rnd.Intn(256)),
				G: uint8(rnd.Intn(256)),
				B: uint8(rnd.Intn(256)),
				A: 255,
			})
		}
	}
	return img
}

// encodeTestJPEG 把图编码成 JPEG 字节，质量给高一点，让原图足够大。
func encodeTestJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// encodeTestPNG 把图编码成 PNG 字节。
func encodeTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// readSaved 按返回的 URL 读回落盘文件，顺便验证 URL 与目录的对应关系没写反。
func readSaved(t *testing.T, dir string, saved *Saved) []byte {
	t.Helper()
	if !strings.HasPrefix(saved.URL, "/uploads/") {
		t.Fatalf("URL 前缀不对: %s", saved.URL)
	}
	// URL 是给浏览器的，一定是正斜杠；磁盘路径在 Windows 上要换回来。
	rel := strings.TrimPrefix(saved.URL, "/uploads/")
	full := filepath.Join(dir, filepath.FromSlash(rel))
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("读回落盘文件 %s: %v", full, err)
	}
	return data
}

func TestSaveAvatarScalesAndCompresses(t *testing.T) {
	s, dir := newService(t, Config{})
	raw := encodeTestJPEG(t, noiseImage(1200, 800))

	saved, err := s.SaveAvatar(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("SaveAvatar: %v", err)
	}

	// 长边压到 256，短边按比例跟着走（1200:800 = 3:2，256 的长边对应 171）。
	if saved.Width != 256 {
		t.Errorf("宽 = %d, 期望 256", saved.Width)
	}
	if saved.Height < 168 || saved.Height > 174 {
		t.Errorf("高 = %d, 期望 170 左右（保持比例）", saved.Height)
	}
	// 这是整件事的目的：一张手机原图级别的图，落盘只剩一小截。
	if saved.Bytes >= saved.OriginalBytes {
		t.Errorf("压缩后 %d 字节，不比原图 %d 小", saved.Bytes, saved.OriginalBytes)
	}
	if saved.Bytes > 60<<10 {
		t.Errorf("压缩后 %d 字节，超过 60 KB，说明缩放或质量参数没生效", saved.Bytes)
	}
	if saved.Format != "jpeg" {
		t.Errorf("格式 = %s, 期望 jpeg", saved.Format)
	}
	if !strings.HasSuffix(saved.URL, ".jpg") {
		t.Errorf("URL = %s, 期望 .jpg 结尾", saved.URL)
	}
	// 路径里要带年月，方便按月归档；这个时间来自被固定的 now。
	if !strings.Contains(saved.URL, "/avatars/2026/11/") {
		t.Errorf("URL = %s, 期望落在 avatars/2026/11 下", saved.URL)
	}

	data := readSaved(t, dir, saved)
	if int64(len(data)) != int64(saved.Bytes) {
		t.Errorf("落盘 %d 字节，返回 %d 字节，两者应当一致", len(data), saved.Bytes)
	}
}

func TestSaveAvatarIsContentAddressed(t *testing.T) {
	s, dir := newService(t, Config{})
	raw := encodeTestJPEG(t, noiseImage(400, 400))

	first, err := s.SaveAvatar(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("第一次上传: %v", err)
	}
	second, err := s.SaveAvatar(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("第二次上传: %v", err)
	}

	// 内容一样，编码结果一样，哈希一样，所以 URL 必须完全相同。
	// 这是「URL 自带版本」的前提：图变了文件名才变，没变就复用同一份缓存。
	if first.URL != second.URL {
		t.Errorf("同一张图两次上传得到不同地址: %s vs %s", first.URL, second.URL)
	}

	// 只应落一个文件：临时文件都 rename 掉了，重复上传也不新增。
	var count int
	root := filepath.Join(dir, "avatars")
	err = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历目录: %v", err)
	}
	if count != 1 {
		t.Errorf("目录里有 %d 个文件，期望 1 个", count)
	}
}

func TestSaveAvatarKeepsRealTransparencyAsPNG(t *testing.T) {
	s, _ := newService(t, Config{})

	// 一半透明一半不透明：这是真的需要透明通道的图（抠图头像、logo）。
	img := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			if x < 150 {
				img.Set(x, y, color.NRGBA{R: 200, G: 30, B: 30, A: 255})
			}
		}
	}

	saved, err := s.SaveAvatar(bytes.NewReader(encodeTestPNG(t, img)))
	if err != nil {
		t.Fatalf("SaveAvatar: %v", err)
	}
	// 压成 JPEG 会把透明区域变成黑块，所以这种图必须留 PNG。
	if saved.Format != "png" || !strings.HasSuffix(saved.URL, ".png") {
		t.Errorf("格式 = %s / URL = %s, 期望 png", saved.Format, saved.URL)
	}
	// PNG 也要缩放，不能因为换了编码就跳过。
	if saved.Width != 256 {
		t.Errorf("宽 = %d, 期望 256", saved.Width)
	}
}

func TestSaveAvatarFlattensStrayAlpha(t *testing.T) {
	s, _ := newService(t, Config{})

	// 只有一个角上有几个半透明像素，其余完全不透明。
	// 截图和修图软件导出的 PNG 经常是这样，按 PNG 存要多付三四倍体积。
	img := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.NRGBA{R: 20, G: 120, B: 200, A: 255})
		}
	}
	img.Set(0, 0, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
	img.Set(1, 0, color.NRGBA{R: 0, G: 0, B: 0, A: 128})

	saved, err := s.SaveAvatar(bytes.NewReader(encodeTestPNG(t, img)))
	if err != nil {
		t.Fatalf("SaveAvatar: %v", err)
	}
	if saved.Format != "jpeg" {
		t.Errorf("格式 = %s, 期望 jpeg（零星透明按不透明处理）", saved.Format)
	}
}

func TestSaveAvatarHonoursEXIFOrientation(t *testing.T) {
	s, _ := newService(t, Config{})

	// 120×60 的「横图」，EXIF 标记 6（需要顺时针转 90 度）。
	// 摆正之后应该变成 60×120，再缩到长边 256 的范围内。
	raw := encodeTestJPEG(t, noiseImage(120, 60))
	raw = withEXIFOrientation(raw, 6)

	saved, err := s.SaveAvatar(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("SaveAvatar: %v", err)
	}

	// 原图 120×60 本身小于 256，不会被缩放，所以宽高直接反映有没有转。
	if saved.Width != 60 || saved.Height != 120 {
		t.Errorf("输出 %dx%d, 期望 60x120（EXIF 方向 6 应交换宽高）", saved.Width, saved.Height)
	}
}

func TestSaveAvatarRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		// raw 为请求体内容
		raw []byte
		// want 是期望命中的哨兵错误
		want error
	}{
		{
			name: "空内容",
			raw:  nil,
			want: ErrEmpty,
		},
		{
			name: "不是图片",
			raw:  []byte("<html>404 not found</html>"),
			want: ErrUnsupported,
		},
		{
			name: "超过大小上限",
			cfg:  Config{MaxBytes: 1024},
			// 一张正常 JPEG 轻松超过 1 KB。
			raw:  encodeTestJPEG(t, noiseImage(200, 200)),
			want: ErrTooLarge,
		},
		{
			name: "像素数超过上限",
			// 把上限调到 100 像素：20x20 的图就是 400 像素，直接超。
			// 不用真的造一张巨大的图，是因为校验发生在解码之前，
			// 只看 DecodeConfig 报出来的宽高。
			cfg:  Config{MaxPixels: 100},
			raw:  encodeTestJPEG(t, noiseImage(20, 20)),
			want: ErrTooLargeImg,
		},
		{
			name: "短边太小",
			raw:  encodeTestJPEG(t, noiseImage(100, 4)),
			want: ErrTooSmallImg,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := newService(t, tc.cfg)
			_, err := s.SaveAvatar(bytes.NewReader(tc.raw))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, 期望命中 %v", err, tc.want)
			}

			// 失败时不能在磁盘上留东西：半张图被浏览器缓存一年，
			// 而且用户看到的是一个坏头像，比没有头像更糟。
			entries, statErr := os.ReadDir(filepath.Join(dir, "avatars"))
			if statErr != nil {
				if os.IsNotExist(statErr) {
					return
				}
				t.Fatalf("读目录: %v", statErr)
			}
			if len(entries) != 0 {
				t.Errorf("失败的上传留下了 %d 个目录项", len(entries))
			}
		})
	}
}

func TestNewServiceValidatesDir(t *testing.T) {
	if _, err := NewService(Config{}); err == nil {
		t.Fatal("Dir 为空时应当返回错误，而不是默默在当前目录建 uploads")
	}
}

func TestNewServiceNormalisesURLPrefix(t *testing.T) {
	// 配置写成 "uploads" 或 "/uploads/" 都应该归一成 "/uploads"，
	// 否则拼出来的地址是 //uploads//avatars 这种能开但难看的东西。
	for _, raw := range []string{"uploads", "/uploads", "/uploads/", " uploads "} {
		s, err := NewService(Config{Dir: t.TempDir(), URLPrefix: raw})
		if err != nil {
			t.Fatalf("NewService(%q): %v", raw, err)
		}
		if s.URLPrefix() != "/uploads" {
			t.Errorf("前缀 %q 归一成 %q, 期望 /uploads", raw, s.URLPrefix())
		}
	}
}

// withEXIFOrientation 往一张合法 JPEG 的 SOI 后面插入只含方向标签的 APP1 段。
// 直接拼字节而不是引库，是为了让用例能精确控制「只有方向、没有别的元数据」。
func withEXIFOrientation(raw []byte, orientation int) []byte {
	// TIFF 结构（小端）：字节序标记 + 魔数 42 + IFD0 偏移 8 + 一个条目。
	tiff := []byte{
		'I', 'I', 42, 0, 8, 0, 0, 0,
		1, 0, // 条目数
		0x12, 0x01, // 标签 0x0112
		3, 0, // 类型 SHORT
		1, 0, 0, 0, // 个数 1
		byte(orientation), 0, 0, 0, // 值（小端前两字节）
		0, 0, 0, 0, // 下一个 IFD 偏移
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)

	seg := []byte{0xFF, 0xE1, 0, 0}
	size := len(payload) + 2
	seg[2] = byte(size >> 8)
	seg[3] = byte(size)

	out := make([]byte, 0, len(raw)+len(payload)+4)
	out = append(out, raw[0], raw[1])
	out = append(out, seg...)
	out = append(out, payload...)
	out = append(out, raw[2:]...)
	return out
}
