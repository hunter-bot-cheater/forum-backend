package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rotateWriter 是一个按大小切分的日志文件写入器。
// 写满 maxBytes 就把 app.log 改名成 app.log.1，旧的依次后移，只保留 maxBackups 份。
//
// 为什么不直接用第三方库：这里只需要「按大小切、留几份、过期的删掉」三件事，
// 用自己的实现能把行为讲清楚，也少一个依赖。需要压缩、按小时切分再换库。
//
// 同一份文件只允许一个进程写。多进程各自持有文件句柄，轮转时互相改名会打架，
// 所以只有 cmd/server 开文件输出，一次性的 migrate / reconcile 只打控制台。
type rotateWriter struct {
	mu sync.Mutex
	// path 是当前正在写的文件名（例如 logs/app.log），备份文件是 path + ".1"、".2"。
	path       string
	maxBytes   int64
	maxBackups int
	maxAge     time.Duration
	file       *os.File
	size       int64
}

// newRotateWriter 打开（或创建）日志文件，接着把目录建好、清理过期备份。
// 返回错误表示目录建不出来或文件没权限写，启动阶段应当直接失败，不要静默丢掉日志。
func newRotateWriter(path string, maxBytes int64, maxBackups int, maxAge time.Duration) (*rotateWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}

	w := &rotateWriter{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		maxAge:     maxAge,
	}

	// 先清理历史备份，再打开当前文件：
	// 进程重启后接续写同一个文件，size 从现有长度开始算，否则会切出一堆小文件。
	if err := w.cleanup(); err != nil {
		return nil, err
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write 实现 io.Writer。写之前先判断这一条会不会把文件撑过上限，
// 超了就先把当前文件轮转掉，再往新文件写。
func (w *rotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// size > 0 的条件不能省：单条日志本身就超过 maxBytes 时，
	// 不判断会陷入「切分 → 还是超 → 再切分」的死循环，不如让它写成一个大文件。
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭底层文件。调用后这个 writer 不能再写。
func (w *rotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// open 以追加模式打开当前日志文件，并把已有内容的长度记到 size。
func (w *rotateWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file %s: %w", w.path, err)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

// rotate 把当前文件挪成 .1，原来的 .1 变成 .2，依此类推，然后新建一个空文件。
// 顺序必须从最旧的开始往前挪，否则 .1 会先被覆盖，历史日志凭空消失。
func (w *rotateWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log file before rotate: %w", err)
	}
	w.file = nil

	// 超出份数的最后一份直接删掉，空出 .maxBackups 这个位置。
	if w.maxBackups > 0 {
		oldest := w.backupPath(w.maxBackups)
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove oldest log backup %s: %w", oldest, err)
		}
		// 从倒数第二份开始向后移一位：.N-1 → .N、...、.1 → .2。
		for i := w.maxBackups - 1; i >= 1; i-- {
			from := w.backupPath(i)
			to := w.backupPath(i + 1)
			if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("shift log backup %s to %s: %w", from, to, err)
			}
		}
	}

	// 当前文件挪成 .1，再重新创建它继续写。
	if err := os.Rename(w.path, w.backupPath(1)); err != nil {
		return fmt.Errorf("rename log file to backup: %w", err)
	}
	return w.open()
}

// backupPath 返回第 n 份备份的文件名，n 从 1 开始，数字越大越旧。
func (w *rotateWriter) backupPath(n int) string {
	return w.path + "." + strconv.Itoa(n)
}

// cleanup 删除超出份数或超过保留天数的备份。
// 只在打开文件时跑一次：进程长期不重启时，靠 rotate 里的份数控制就够，
// 不带时间判断是为了让「刚写满的文件」不会因为机器时钟异常被立刻删掉。
func (w *rotateWriter) cleanup() error {
	backups, err := filepath.Glob(w.path + ".*")
	if err != nil {
		return fmt.Errorf("list log backups: %w", err)
	}

	type backup struct {
		path    string
		index   int
		modTime time.Time
	}
	kept := make([]backup, 0, len(backups))
	deadline := time.Time{}
	if w.maxAge > 0 {
		deadline = time.Now().Add(-w.maxAge)
	}

	for _, path := range backups {
		suffix := strings.TrimPrefix(path, w.path+".")
		index, err := strconv.Atoi(suffix)
		if err != nil || index <= 0 {
			// 文件名对不上 .N 格式的（比如手工放的 .gz）不归我们管，跳过。
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat log backup %s: %w", path, err)
		}
		if !deadline.IsZero() && info.ModTime().Before(deadline) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove expired log backup %s: %w", path, err)
			}
			continue
		}
		kept = append(kept, backup{path: path, index: index, modTime: info.ModTime()})
	}

	if w.maxBackups <= 0 {
		return nil
	}

	// 剩下的按序号从小到大排：.1 是最新的，序号越大越旧。
	// 保留最前面的 maxBackups 份，后面的（更旧的）删掉。
	// 排序方向不能写反：反过来会把最新的几份删掉、留下最旧的，等于丢了最近的日志。
	sort.Slice(kept, func(i, j int) bool { return kept[i].index < kept[j].index })
	for i := w.maxBackups; i < len(kept); i++ {
		if err := os.Remove(kept[i].path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove excess log backup %s: %w", kept[i].path, err)
		}
	}
	return nil
}
