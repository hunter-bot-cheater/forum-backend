// Package upload 接收用户上传的图片：校验、按 EXIF 摆正、缩放、重编码，再按内容哈希落盘。
//
// 为什么服务端要重编码，而不是把原图原样存下来：
// 用户从手机或图站拿来的图动辄 2~5 MB，而头像在页面上最大只显示 76 px
// （个人主页），列表里是 24 px。原样存下来，每次浏览都要多传几十倍的无用字节。
// 这里统一缩到最长边 256 px（覆盖 3 倍屏），一张头像通常落在 10~25 KB。
//
// 文件名取编码结果的 sha256 前 16 位，于是 URL 天然带内容版本：
// 同一张图重复上传不会产生第二个文件，内容一变文件名就变，
// 浏览器与 CDN 的旧缓存不会被错误复用，也就不需要任何主动清理缓存的逻辑。
package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	// 三个解码器用匿名导入注册到 image.Decode。
	// gif 只取第一帧：头像不动，动图按静态首帧处理。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// 上限与默认值。放在这里而不是散在调用处，
// 是因为它们同时决定「服务能承受多大的输入」和「用户等多久」，改的时候要一起看。
const (
	// defaultMaxBytes 是原始文件的上限。5 MB 足够覆盖手机原图，
	// 再大的多半是没压缩的截图或设计稿，让客户端先自己处理。
	defaultMaxBytes = 5 << 20

	// defaultMaxDimension 是输出图的最长边。
	// 页面上最大的头像是个人主页的 76 px，三倍屏要 228 px，256 留了一点余量。
	defaultMaxDimension = 256

	// defaultJPEGQuality 是重编码质量。82 是肉眼几乎看不出损失、
	// 体积又明显小于 90 的位置；头像尺寸小，压缩痕迹本来就不明显。
	defaultJPEGQuality = 82

	// defaultMaxPixels 是解码前的像素上限，防「解压炸弹」：
	// 一个几百 KB 的 PNG 可以解出几亿像素，直接吃掉几个 G 内存。
	// 1200 万像素对应 4000×3000，正好是手机原图的常见尺寸。
	defaultMaxPixels = 12_000_000

	// minDimension 是输出图的短边下限。小于它的图放大没有意义，
	// 但 1×1 的图会让圆形头像变成一个小点，直接拒掉更好解释。
	minDimension = 8
)

// 上传失败的原因。handler 按这几种分别映射成 400 / 413 / 415，
// 用字符串匹配会随文案改动失效。
var (
	ErrEmpty       = errors.New("upload: empty body")
	ErrTooLarge    = errors.New("upload: file exceeds size limit")
	ErrUnsupported = errors.New("upload: unsupported image format")
	ErrTooLargeImg = errors.New("upload: image dimensions exceed limit")
	ErrTooSmallImg = errors.New("upload: image is too small")
)

// Config 是上传模块的运行参数，对应 config.yaml 的 upload 段。
// 零值字段由 NewService 填成默认值，所以配置里只写想改的那几项。
type Config struct {
	// Dir 是落盘根目录，容器里挂数据卷。相对路径按进程工作目录解析。
	Dir string
	// URLPrefix 是对外访问前缀，默认 /uploads。
	// 生产由 Nginx 直接读同一份目录，请求根本不会到后端。
	URLPrefix string
	// MaxBytes 是单次上传的原始文件上限（字节）。
	MaxBytes int64
	// MaxDimension 是输出图最长边（像素）。
	MaxDimension int
	// JPEGQuality 是不透明图重编码的 JPEG 质量，1~100。
	JPEGQuality int
	// MaxPixels 是解码前允许的最大像素数。
	MaxPixels int
}

// Service 把上传的图片处理并写进 Dir。
//
// 没有可变状态：所有字段在构造时定死，因此可以并发调用，不需要加锁。
type Service struct {
	dir          string
	urlPrefix    string
	maxBytes     int64
	maxDimension int
	jpegQuality  int
	maxPixels    int
	// now 只在生成日期分目录时用，测试里替换成固定时间来断言路径形状。
	now func() time.Time
}

// NewService 校验参数、补齐默认值，并建好根目录。
//
// 目录在启动时创建而不是等到第一次上传：磁盘没挂上、权限不对这类问题
// 应该在启动日志里暴露，而不是让第一个上传的用户吃一个 500。
func NewService(cfg Config) (*Service, error) {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		return nil, errors.New("upload: dir is required")
	}

	prefix := strings.TrimSpace(cfg.URLPrefix)
	if prefix == "" {
		prefix = "/uploads"
	}
	// 统一成「前面有斜杠、后面没有」：拼接时才能直接用 path.Join 得到干净的结果，
	// 否则会出现 //uploads//avatars 这种能访问但难看的地址。
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimSuffix(prefix, "/")

	s := &Service{
		dir:          dir,
		urlPrefix:    prefix,
		maxBytes:     cfg.MaxBytes,
		maxDimension: cfg.MaxDimension,
		jpegQuality:  cfg.JPEGQuality,
		maxPixels:    cfg.MaxPixels,
		now:          time.Now,
	}
	if s.maxBytes <= 0 {
		s.maxBytes = defaultMaxBytes
	}
	if s.maxDimension <= 0 {
		s.maxDimension = defaultMaxDimension
	}
	if s.jpegQuality <= 0 || s.jpegQuality > 100 {
		s.jpegQuality = defaultJPEGQuality
	}
	if s.maxPixels <= 0 {
		s.maxPixels = defaultMaxPixels
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("upload: create dir %s: %w", dir, err)
	}
	return s, nil
}

// MaxBytes 返回当前配置的单次上传上限，供路由层设置请求体限制。
// 路由上的 limitBody 必须比它略大：multipart 的分隔符与字段名也占字节。
func (s *Service) MaxBytes() int64 {
	return s.maxBytes
}

// URLPrefix 返回对外访问前缀（形如 /uploads），供路由注册静态目录。
func (s *Service) URLPrefix() string {
	return s.urlPrefix
}

// Saved 是一次上传的结果。
//
// OriginalBytes 与 Bytes 一起返回，是为了让前端能显示「267 KB → 24 KB」。
// 用户看到这个差值才会知道服务端确实压过了，而不是以为图被弄坏了。
type Saved struct {
	URL           string `json:"url"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Bytes         int    `json:"bytes"`
	OriginalBytes int    `json:"original_bytes"`
	// Format 是最终落盘的编码格式：jpeg 或 png。
	// 带透明通道的图必须留 png，压成 jpeg 会把透明区域变成黑色。
	Format string `json:"format"`
}

// SaveAvatar 处理一张头像并落盘，返回可访问的相对地址。
//
// 整个过程全部在内存里完成，不读盘也不查库：
// 输入上限 5 MB，输出上限 256 px，对单核来说也是毫秒级。
func (s *Service) SaveAvatar(r io.Reader) (*Saved, error) {
	// 多读一个字节用来判断是否超限：LimitReader 本身不会报错，
	// 读到的长度正好等于上限时无法区分「刚好」和「被截断」。
	raw, err := io.ReadAll(io.LimitReader(r, s.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("upload: read body: %w", err)
	}
	if len(raw) == 0 {
		return nil, ErrEmpty
	}
	if int64(len(raw)) > s.maxBytes {
		return nil, ErrTooLarge
	}

	// 先只解头部拿尺寸。真正解码前必须知道像素数，
	// 否则一张 50000×50000 的 PNG 会先把内存吃光再谈校验。
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if !supportedFormat(format) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > s.maxPixels {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooLargeImg, cfg.Width, cfg.Height)
	}
	if cfg.Width < minDimension || cfg.Height < minDimension {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooSmallImg, cfg.Width, cfg.Height)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}

	// JPEG 的方向信息在 EXIF 里，image.Decode 不认。
	// 不摆正的话，手机横着拍的照片存成头像后是躺着的，
	// 而原图的 EXIF 又被我们重编码丢掉了，用户再也没法自己转回来。
	if orientation := jpegOrientation(raw); orientation != 1 {
		img = applyOrientation(img, orientation)
	}

	out := fitWithin(img, s.maxDimension)

	// 透明判定放在缩放之后：缩放后的图只有 256 px 见方，
	// 遍历一遍几十万个像素，比在原图上扫几百万个便宜得多。
	hasAlpha, keepAlpha := alphaStats(out)
	var encoded []byte
	var ext string
	var outFormat string
	if keepAlpha {
		encoded, err = encodePNG(out)
		ext = ".png"
		outFormat = "png"
	} else {
		// 有零星的半透明边缘（截图、修图软件导出常见）时先合到白底再压 JPEG：
		// 不合成的话，JPEG 编码会忽略 alpha，透明区域的 RGB 值（通常是 0）
		// 会直接变成黑块，头像像是被啃掉一块。
		flat := out
		if hasAlpha {
			flat = flattenOnWhite(out)
		}
		encoded, err = encodeJPEG(flat, s.jpegQuality)
		ext = ".jpg"
		outFormat = "jpeg"
	}
	if err != nil {
		return nil, err
	}

	name := contentName(encoded) + ext
	// 日期分目录只为「翻目录时能按时间归档」，与缓存无关：
	// URL 的版本来自文件名里的内容哈希，不是日期。
	sub := filepath.Join("avatars", s.now().Format("2006"), s.now().Format("01"))
	if err := s.write(filepath.Join(sub, name), encoded); err != nil {
		return nil, err
	}

	bounds := out.Bounds()
	return &Saved{
		// URL 一律用正斜杠：这是给浏览器用的地址，不能跟着 Windows 走反斜杠。
		URL:           path.Join(s.urlPrefix, filepath.ToSlash(sub), name),
		Width:         bounds.Dx(),
		Height:        bounds.Dy(),
		Bytes:         len(encoded),
		OriginalBytes: len(raw),
		Format:        outFormat,
	}, nil
}

// supportedFormat 是 image.Decode 报出来的格式名白名单。
// 解码器注册了哪些不等于愿意收哪些：动图、TIFF 这类要么处理起来有歧义，
// 要么体积换不来收益，收进来只会增加攻击面。
func supportedFormat(format string) bool {
	switch format {
	case "jpeg", "png", "gif", "webp":
		return true
	}
	return false
}

// contentName 取编码结果的 sha256 前 16 位作为文件名。
//
// 16 位十六进制是 64 bit，撞一次的概率在「每天传一千张、传一百年」的量级下
// 仍然可以忽略；而文件名短一半，URL 也短一半。
func contentName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// write 把内容原子地写到 dir/rel。
//
// 先写同目录的临时文件再 rename：直接往目标文件写，
// 中途进程被重启会留下半张图，而浏览器会把这张半图缓存一年（URL 带哈希、
// 内容不会变，这是缓存策略的前提）。rename 在同一文件系统上是原子的，
// 读到的要么没有，要么是完整的。
func (s *Service) write(rel string, data []byte) error {
	full := filepath.Join(s.dir, rel)
	// 已经存在说明是同一张图重复上传（文件名就是内容哈希），直接复用。
	if _, err := os.Stat(full); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("upload: create dir: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return fmt.Errorf("upload: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("upload: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("upload: close temp: %w", err)
	}
	// 临时文件默认 0600，而 Nginx 是以另一个用户读它的，必须放到 0644。
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("upload: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("upload: rename: %w", err)
	}
	return nil
}
