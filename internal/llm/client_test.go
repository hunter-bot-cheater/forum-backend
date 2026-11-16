package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient 起一个假的大模型服务，返回它和记录下来的请求。
//
// 不连真实供应商：单测不该依赖外部网络、密钥和额度，
// 而且这里要验的正是「我们发了什么、怎么解析返回」，假服务能精确控制这两端。
func newTestClient(t *testing.T, status int, reply string) (*Client, *chatRequest) {
	t.Helper()

	captured := &chatRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 路径必须带上 /chat/completions：少了它说明 baseURL 拼接写错，
		// 真实环境会 404，而单测里如果不校验就会一直是绿的。
		if r.URL.Path != "/v4/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected authorization %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, captured); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		payload, err := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": reply}},
			},
		})
		if err != nil {
			t.Errorf("encode fake response: %v", err)
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	client := NewClient(Options{
		BaseURL: srv.URL + "/v4/",
		APIKey:  "test-key",
		Model:   "glm-test",
	})
	return client, captured
}

// TestSuggestTagSendsTitleAndContent 校验请求体的关键字段：
// 模型名、system 提示、以及标题正文确实被发出去了。
// 这些写错了不会报错，只会静默生成一个与文章无关的标签。
func TestSuggestTagSendsTitleAndContent(t *testing.T) {
	client, got := newTestClient(t, http.StatusOK, "后端开发")

	tag, err := client.SuggestTag(context.Background(), "MySQL 索引怎么建", "讲联合索引的最左前缀。")
	if err != nil {
		t.Fatalf("SuggestTag: %v", err)
	}
	if tag != "后端开发" {
		t.Fatalf("tag = %q, want 后端开发", tag)
	}
	if got.Model != "glm-test" {
		t.Errorf("model = %q", got.Model)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if !strings.Contains(got.Messages[1].Content, "MySQL 索引怎么建") || !strings.Contains(got.Messages[1].Content, "最左前缀") {
		t.Errorf("user message lost title or content: %q", got.Messages[1].Content)
	}
}

// TestSuggestTagClipsLongContent 长正文只发前 800 字：
// 全发会显著增加延迟和费用，而起标签并不需要读完全文。
func TestSuggestTagClipsLongContent(t *testing.T) {
	client, got := newTestClient(t, http.StatusOK, "生活随记")

	// 1000 个汉字，超过 maxContentRunes。
	if _, err := client.SuggestTag(context.Background(), "标题", strings.Repeat("字", 1000)); err != nil {
		t.Fatalf("SuggestTag: %v", err)
	}
	// 正文被截到 800 字，加上「标题：标题\n\n正文：」十几字，总数远小于 1000。
	if n := len([]rune(got.Messages[1].Content)); n > maxContentRunes+30 {
		t.Fatalf("content not clipped, runes = %d", n)
	}
}

// TestSuggestTagFailure 覆盖三种「拿不到标签」：状态码错、没有 choices、内容为空。
// 它们都必须以 error 返回，让上层记日志并放弃标签，而不是往库里写一个空串。
func TestSuggestTagFailure(t *testing.T) {
	t.Run("非 200", func(t *testing.T) {
		client, _ := newTestClient(t, http.StatusUnauthorized, "")
		_, err := client.SuggestTag(context.Background(), "标题", "正文")
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("err = %v, want 401", err)
		}
	})

	t.Run("没有 choices", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// 真实供应商在触发内容审核时就是回这种空 choices。
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}))
		t.Cleanup(srv.Close)
		client := NewClient(Options{BaseURL: srv.URL, APIKey: "k", Model: "m"})

		if _, err := client.SuggestTag(context.Background(), "标题", "正文"); err == nil {
			t.Fatal("expected error for empty choices")
		}
	})

	t.Run("内容是空白", func(t *testing.T) {
		client, _ := newTestClient(t, http.StatusOK, "   \n")
		if _, err := client.SuggestTag(context.Background(), "标题", "正文"); err == nil {
			t.Fatal("expected error for blank tag")
		}
	})
}

// TestSanitizeTag 覆盖模型输出的各种跑偏形态。
// 每一条都是线上真实会遇到的样子（加前缀、加井号、带句号、换行补解释），
// 清洗掉了就能用，清洗不掉就等于这篇没有标签。
func TestSanitizeTag(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"干净输出", "后端开发", "后端开发"},
		{"带井号", "#后端开发", "后端开发"},
		{"带标签前缀", "标签：后端开发", "后端开发"},
		{"英文冒号前缀", "Tag: backend", "backend"},
		{"句末带句号", "后端开发。", "后端开发"},
		{"换行加解释", "后端开发\n这篇主要讲索引优化", "后端开发"},
		{"中文引号包裹", "「数据库」", "数据库"},
		{"书名号包裹", "《职场》", "职场"},
		{"编号开头", "1. 数据库", "数据库"},
		{"中间多余空格", "Go   语言", "Go 语言"},
		{"首尾空白", "  生活随记  ", "生活随记"},
		{"空串", "", ""},
		{"只有标点", "。", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeTag(tc.in); got != tc.want {
				t.Errorf("sanitizeTag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeTagClipsAtColumnLimit 超长标签截到 32 字符：
// tags.name 是 varchar(32)，不截断会让整条 INSERT 失败，连带文章也发不出去。
func TestSanitizeTagClipsAtColumnLimit(t *testing.T) {
	got := sanitizeTag(strings.Repeat("长", 50))
	if n := len([]rune(got)); n != maxTagRunes {
		t.Fatalf("rune len = %d, want %d", n, maxTagRunes)
	}
}
