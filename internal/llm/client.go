// Package llm 封装「让大模型替文章起一个标签」这一件事。
//
// 只有一个用途、一个方法，所以不引入任何 SDK：请求体是 OpenAI 兼容的
// /chat/completions，智谱、DeepSeek、本地 vLLM 都认这套协议，
// 换供应商只改 base_url 与 model 两个配置项，这个文件不用动。
//
// 它刻意不知道「文章」是什么：入参就是标题与正文两段文本，
// 这样 article 包只依赖一个一方法接口，测试里塞个假实现即可，不必启动 HTTP。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Options 是构造 Client 的全部参数，由 router 从 config.LLMConfig 映射而来。
type Options struct {
	// BaseURL 只到版本段，例如 https://open.bigmodel.cn/api/paas/v4，
	// 结尾带不带斜杠都行，构造时会去掉。
	BaseURL string
	// APIKey 走 Authorization: Bearer，不要放进 URL 查询串——
	// 查询串会出现在访问日志和代理的 trace 里。
	APIKey string
	Model  string
	// Timeout 是单次生成的硬上限，包含建连、等待与读响应体。
	// 传 0 时用 defaultTimeout。
	Timeout time.Duration
}

// defaultTimeout 与配置里的默认值保持一致。
// 短文本分类正常在 1 秒内返回，8 秒还没回来说明链路有问题。
const defaultTimeout = 8 * time.Second

// maxContentRunes 是发给模型的正文长度上限。
//
// 起标签只要有主题就够，把整篇一万多字的文章塞进去只增加延迟与费用；
// 800 字足够判断「这是一篇讲数据库索引的技术文」还是「一篇生活随记」。
const maxContentRunes = 800

// maxTagRunes 与 article 包的 maxTagRunes 对齐（tags.name 是 varchar(32)）。
// 模型偶尔会输出一整句，超出部分直接截掉，让清洗后的结果一定能落库。
const maxTagRunes = 32

// Client 是调用 chat/completions 的极简客户端，可以并发使用。
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
}

// NewClient 构造客户端。调用方负责判断配置是否完整（开关打开且密钥非空），
// 这里不做校验：没有密钥时请求会拿到 401，错误会带着状态码返回给调用方记日志。
func NewClient(opt Options) *Client {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		// 超时放在 http.Client 上而不是每次请求建一个 context：
		// 它同时管住建连和读 body，后者用 context 管不住（body 读一半卡住不会退出）。
		http:    &http.Client{Timeout: timeout},
		baseURL: strings.TrimRight(opt.BaseURL, "/"),
		apiKey:  opt.APIKey,
		model:   opt.Model,
	}
}

// systemPrompt 把输出格式约束死。
//
// 这里不放用户内容，只放规则：标题正文里的「忽略以上指令」骗不到它，
// 但这只是减少干扰，不是安全边界——返回值最终还会被 sanitizeTag 清洗，
// 而且只用于给标签赋值，拿不到任何执行能力。
const systemPrompt = `你是社区论坛的编辑，负责给文章归类。
读标题和正文，给出一个最贴切的分类标签。
要求：
1. 只输出标签本身，不要任何解释、不要前缀、不要引号；
2. 2 到 6 个汉字，实在没有中文对应词时可以是英文技术名词；
3. 不要加井号，不要用句号结尾。
例如：后端开发、数据库、职场、生活随记、前端框架。`

// chatRequest / chatResponse 只声明用得到的字段。
//
// 不引 SDK 的代价是响应结构要自己写，好处是没人会不小心把整个响应
// （含 usage、id）打进日志。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// SuggestTag 让模型给一篇文章起一个标签，返回已清洗好的标签名。
//
// 返回空串与 error 时调用方应当放弃标签、照常完成发布：
// 标签是附加信息，模型不可用不该让用户发不出文章。
// ctx 用于随请求一起取消（客户端断开发布请求时不用继续等模型）。
func (c *Client) SuggestTag(ctx context.Context, title, content string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			// 标题单独一行、正文跟在后面：模型对「标题：」这种标记很敏感，
			// 比把两段直接拼在一起更容易只抓主题。
			{Role: "user", Content: "标题：" + title + "\n\n正文：" + clipRunes(content, maxContentRunes)},
		},
		// 温度调低：同一篇文章两次生成应当得到同一个标签，
		// 否则用户编辑一次、保存一次，标签就换了。
		Temperature: 0.2,
		// 一个标签最多几个字，24 个 token 已经足够，多了纯属浪费。
		MaxTokens: 24,
	})
	if err != nil {
		return "", fmt.Errorf("encode llm request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build llm request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call llm: %w", err)
	}
	// 不 defer 关闭：这个函数只有一次请求，defer 会把连接占到大模型返回之后才释放；
	// 显式关掉能让底层连接立刻回到连接池。errcheck 要求检查返回值，用 nolint 说明。
	defer func() {
		//nolint:errcheck // 关闭响应体的错误不影响已经读到的结果，也没有可补救的动作。
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		// 只读前 200 字节：供应商的错误体可能很长（含整段 HTML 错误页），
		// 全读进来塞进日志没有意义，状态码加开头就够定位（401 是密钥错，429 是限额）。
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", fmt.Errorf("llm returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	// 限制读取大小：正常响应几百字节，读 64KB 是为了防止对端返回异常大的内容把内存吃掉。
	var decoded chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode llm response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}

	tag := sanitizeTag(decoded.Choices[0].Message.Content)
	if tag == "" {
		return "", fmt.Errorf("llm returned an empty tag")
	}
	return tag, nil
}

// sanitizeTag 把模型的自由输出压成一个能落库的标签。
//
// 强调「自由」：提示词写了「只输出标签」，但模型仍可能回「标签：后端开发。」、
// 「#后端开发」或者换行后再补一句解释。这些都不该让整次生成作废，
// 能救回来的就地救回来；救不回来（清洗后为空）才返回空串让调用方放弃。
func sanitizeTag(raw string) string {
	// 只取第一行：换行之后的多半是「解释」或「其他可选标签」，
	// 拼进来会变成一个几十字的怪标签。
	line := raw
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)

	// 「标签：后端开发」「Tag: backend」这类前缀，取冒号后的部分。
	// 从后往前找最后一个冒号：模型偶尔会写成「分类：技术：后端」，要的是最后一段。
	//
	// 这里必须转成 []rune 再切：strings.LastIndexAny 返回的是字节下标，
	// 中文冒号占 3 个字节，拿字节下标 +1 去切会落在冒号的第二个字节上，
	// 结果是一个乱码字符顶上标签开头（实测 "\\xbc\\x9a后端开发"）。
	runes := []rune(line)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '：' || runes[i] == ':' {
			line = strings.TrimSpace(string(runes[i+1:]))
			break
		}
	}

	// 去掉包裹与装饰：#、引号、书名号、方括号。
	line = strings.TrimLeft(line, "#＃")
	line = strings.Trim(line, `"'“”‘’《》「」『』【】[] `)
	// 去掉中文列表符号「1. 」「- 」这类开头，模型有时会带编号。
	line = strings.TrimLeft(line, "-—·0123456789.、) ）")
	// 去掉句末标点：标签是一个词，不该以句号或逗号结尾。
	line = strings.TrimRight(line, "。.，,、；;！!？?…~～")
	line = strings.TrimSpace(line)

	// 把中间的连续空白（含全角空格）压成单个半角空格：
	// 「Go   语言」和「Go 语言」在页面上看着一样，落库却是两个不同的标签。
	line = strings.Join(strings.Fields(line), " ")

	return clipRunes(line, maxTagRunes)
}

// clipRunes 按字符截断，不会把汉字切成半个。
// 超出时不留省略号：截断后的结果要当作完整标签使用，带省略号反而更怪。
func clipRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
