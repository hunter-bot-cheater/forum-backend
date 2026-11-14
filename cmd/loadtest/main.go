// Command loadtest 是一个最小可用的压测客户端。
//
// 只依赖标准库，避免为了拿一组数字去装 vegeta 或 k6。
// 它做四件事：固定并发打一段时间、记录每个请求的耗时、按状态码分类、输出分位数。
//
// 用法示例：
//
//	go run ./cmd/loadtest -url "http://127.0.0.1:8080/api/articles/1" -concurrency 50 -duration 15s
//	go run ./cmd/loadtest -url "http://127.0.0.1:8080/api/articles/{id}" -ids 1-1000 -concurrency 100
//	go run ./cmd/loadtest -url "http://127.0.0.1:8080/api/articles/hot" -concurrency 50
//
// 它不会替你绕开限流。同一个 IP 打 /api，Capacity=10 的令牌桶会先把 429 打出来，
// 这时候测到的是限流器，不是服务端容量。压测前先处理限流配置，见 05a 文档第 2 节。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sample 是一个协程的本地统计。
// 每个协程自己 append，结束再合并，压测过程中不加锁。
type sample struct {
	latencies []time.Duration
	codes     map[int]int
	netErr    int
}

type requestConfig struct {
	url         string
	method      string
	body        string
	contentType string
	token       string
}

func main() {
	os.Exit(run())
}

func run() int {
	url := flag.String("url", "", "目标地址，可含 {id} 占位符")
	method := flag.String("method", "GET", "HTTP 方法")
	duration := flag.Duration("duration", 10*time.Second, "压测时长")
	concurrency := flag.Int("concurrency", 50, "并发协程数")
	idsFlag := flag.String("ids", "", "id 池，形如 1-1000 或 1,2,3，配合 {id} 使用")
	token := flag.String("token", "", "Bearer token，压测需要登录的接口时传")
	body := flag.String("body", "", "请求体")
	contentType := flag.String("content-type", "application/json", "请求 Content-Type")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -url")
		return 2
	}
	if *concurrency < 1 || *concurrency > 2000 {
		fmt.Fprintln(os.Stderr, "-concurrency 取值 1 到 2000")
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "-duration 必须大于 0")
		return 2
	}

	ids, err := parseIDs(*idsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析 -ids 失败: %v\n", err)
		return 2
	}
	useID := strings.Contains(*url, "{id}")
	if useID && len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "URL 里用了 {id}，必须同时给 -ids")
		return 2
	}

	cfg := requestConfig{
		url:         *url,
		method:      *method,
		body:        *body,
		contentType: *contentType,
		token:       *token,
	}

	// 默认的 MaxIdleConnsPerHost 是 2。
	// 并发 50 而空闲连接只有 2 个时，压测客户端自己在反复建 TCP 连接，
	// 测出来的延迟里大半是客户端握手成本，不是服务端处理时间。
	transport := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency,
		MaxConnsPerHost:     *concurrency,
		IdleConnTimeout:     30 * time.Second,
		// 压测不关心响应体内容，也不该让客户端花 CPU 解压。
		DisableCompression: true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}
	defer transport.CloseIdleConnections()

	warmup(client, cfg, ids, useID)

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	startedAt := time.Now()
	samples := make([]*sample, *concurrency)
	var wg sync.WaitGroup
	wg.Add(*concurrency)
	for i := 0; i < *concurrency; i++ {
		s := &sample{
			latencies: make([]time.Duration, 0, 1024),
			codes:     make(map[int]int),
		}
		samples[i] = s
		go func(s *sample) {
			defer wg.Done()
			worker(ctx, client, cfg, ids, useID, s)
		}(s)
	}
	wg.Wait()
	elapsed := time.Since(startedAt)

	report(samples, elapsed)
	return 0
}

// warmup 先串行打几个请求，把 TCP 连接与缓存都准备好。
// 不预热的话，前几十个样本里混着建连成本与冷缓存回源，P99 会被它们拉偏。
func warmup(client *http.Client, cfg requestConfig, ids []uint, useID bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	times := 20
	if len(ids) > 0 && len(ids) < times {
		times = len(ids)
	}
	for i := 0; i < times; i++ {
		target := cfg.url
		if useID {
			id := ids[i%len(ids)]
			target = strings.Replace(cfg.url, "{id}", strconv.FormatUint(uint64(id), 10), 1)
		}
		req, err := http.NewRequestWithContext(ctx, cfg.method, target, nil)
		if err != nil {
			return
		}
		if cfg.token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func worker(ctx context.Context, client *http.Client, cfg requestConfig, ids []uint, useID bool, s *sample) {
	for {
		if ctx.Err() != nil {
			return
		}

		target := cfg.url
		if useID {
			id := ids[rand.IntN(len(ids))]
			target = strings.Replace(cfg.url, "{id}", strconv.FormatUint(uint64(id), 10), 1)
		}

		var reader io.Reader
		if cfg.body != "" {
			reader = strings.NewReader(cfg.body)
		}

		req, err := http.NewRequestWithContext(ctx, cfg.method, target, reader)
		if err != nil {
			s.netErr++
			return
		}
		if cfg.body != "" {
			req.Header.Set("Content-Type", cfg.contentType)
		}
		if cfg.token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.token)
		}

		start := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(start)

		if err != nil {
			if ctx.Err() != nil {
				// 压测结束导致的取消，不算错误样本。
				return
			}
			s.netErr++
			// 连接被拒时不留退避会纯空转：1 秒能打上万次失败，
			// 把本机 CPU 吃满，反而看不清到底是配置错还是服务没起。
			// 让出 1ms，失败场景下每秒最多几百次，够看清提示。
			time.Sleep(time.Millisecond)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		s.latencies = append(s.latencies, elapsed)
		s.codes[resp.StatusCode]++
	}
}

func report(samples []*sample, elapsed time.Duration) {
	all := make([]time.Duration, 0, 4096)
	codes := make(map[int]int)
	netErr := 0
	for _, s := range samples {
		all = append(all, s.latencies...)
		netErr += s.netErr
		for code, n := range s.codes {
			codes[code] += n
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	total := len(all)
	qps := 0.0
	if elapsed.Seconds() > 0 {
		qps = float64(total) / elapsed.Seconds()
	}

	fmt.Printf("duration   %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("requests   %d\n", total)
	fmt.Printf("qps        %.1f\n", qps)
	fmt.Printf("failed     %d\n", netErr)
	fmt.Printf("latency    p50=%s p90=%s p99=%s max=%s\n",
		percentile(all, 0.50), percentile(all, 0.90), percentile(all, 0.99), percentile(all, 1.0))

	if len(codes) > 0 {
		keys := make([]int, 0, len(codes))
		for code := range codes {
			keys = append(keys, code)
		}
		sort.Ints(keys)
		parts := make([]string, 0, len(keys))
		for _, code := range keys {
			parts = append(parts, fmt.Sprintf("%d=%d", code, codes[code]))
		}
		fmt.Printf("status     %s\n", strings.Join(parts, " "))
	}

	if total == 0 {
		fmt.Println("提示       一个样本都没有，先确认服务是否启动、URL 是否正确")
	}
	if netErr > 0 && netErr*10 > total {
		fmt.Println("提示       网络错误占比超过 10%，客户端连接池或本机端口可能先耗尽了")
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// parseIDs 支持两种写法：连续的 1-1000，和列举的 1,2,3。
// 空字符串返回 nil，表示不用 id 池。
func parseIDs(raw string) ([]uint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	if strings.Contains(raw, "-") {
		parts := strings.SplitN(raw, "-", 2)
		begin, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("起始值 %q 不是数字", parts[0])
		}
		end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("结束值 %q 不是数字", parts[1])
		}
		if begin < 1 || end < begin {
			return nil, fmt.Errorf("区间 %d-%d 不合法", begin, end)
		}
		if end-begin+1 > 100000 {
			return nil, fmt.Errorf("区间太大，一次最多 100000 个 id")
		}
		ids := make([]uint, 0, end-begin+1)
		for i := begin; i <= end; i++ {
			ids = append(ids, uint(i))
		}
		return ids, nil
	}

	fields := strings.Split(raw, ",")
	ids := make([]uint, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%q 不是数字", f)
		}
		if v < 1 {
			return nil, fmt.Errorf("id 必须大于 0，收到 %d", v)
		}
		ids = append(ids, uint(v))
	}
	return ids, nil
}
