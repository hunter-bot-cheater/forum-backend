package article

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"forum-backend/internal/testutil"
)

// fakeTagger 是 TagSuggester 的假实现。
//
// 用它而不是真的 llm.Client：这里要验的是「什么时候调、返回结果怎么处理」，
// 不该依赖外网和密钥。calls 用来断言「用户自己填了标签时不许调模型」——
// 那条只能靠调用次数证明，看返回值是看不出来的。
type fakeTagger struct {
	tag   string
	err   error
	calls int
	// onCall 在「模型思考期间」插一段动作，用来模拟并发。
	// 真实世界里这段时间是 0.5~8 秒，作者完全可能在这期间补上标签。
	onCall func()
}

func (f *fakeTagger) SuggestTag(_ context.Context, _, _ string) (string, error) {
	f.calls++
	if f.onCall != nil {
		f.onCall()
	}
	return f.tag, f.err
}

// pendingTagJobs 数一下补标签 Stream 里还剩几条任务。
//
// 用 XLEN 而不是消费一遍：这里要验的是「有没有投递」，
// 真的消费会把 miniredis 里那条消息 ACK 掉，反而看不出投递了几条。
func pendingTagJobs(t *testing.T, rdb *redis.Client) int64 {
	t.Helper()
	n, err := rdb.XLen(context.Background(), TagJobStream).Result()
	if err != nil {
		t.Fatalf("读取 Stream 长度: %v", err)
	}
	return n
}

// TestCreateEnqueuesTagJobWhenNoneGiven 验证「一个标签都不填」时只投任务、不调模型。
//
// 两件事都要断言：发布路径上模型调用次数必须是 0（发布不能被模型拖慢），
// Stream 里必须正好多出一条带这篇文章 id 的任务（否则标签永远不会被补上）。
func TestCreateEnqueuesTagJobWhenNoneGiven(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "后端开发"}
	svc := NewArticleService(db, rdb, tagger)
	author := testutil.SeedUser(t, db, "autotag")

	created, err := svc.Create(ctx, author.ID, "MySQL 索引怎么建", "讲联合索引的最左前缀。", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tagger.calls != 0 {
		t.Fatalf("发布路径不该同步调模型，实际调了 %d 次", tagger.calls)
	}
	if len(created.Tags) != 0 {
		t.Fatalf("同步返回时不该有标签，实际 %v", created.Tags)
	}

	if n := pendingTagJobs(t, rdb); n != 1 {
		t.Fatalf("补标签任务数 = %d，期望 1", n)
	}
	// 消息体的字段名与 worker 里取的 key 必须一致，拼错了 worker 会当成无 id 消息丢掉。
	// XRange 取回来一律是字符串（Redis 里只有 bulk string），所以两边都转成文本比。
	msgs, err := rdb.XRange(ctx, TagJobStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("读取任务: %v", err)
	}
	if got := fmt.Sprint(msgs[0].Values["id"]); got != fmt.Sprint(created.ID) {
		t.Fatalf("任务里的文章 id = %s，期望 %d", got, created.ID)
	}
}

// TestCreateKeepsManualTagsWithoutEnqueue 验证用户填了标签就既不投任务也不碰模型。
//
// 这条不只是省一次请求：如果这里还投了任务，worker 会看到「已经有标签」而跳过，
// 白跑一轮；更糟的是以后有人改了 worker 的跳过判断，手填的标签就可能被覆盖。
func TestCreateKeepsManualTagsWithoutEnqueue(t *testing.T) {
	db := testutil.NewDB(t)
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "不该被用到的标签"}
	svc := NewArticleService(db, rdb, tagger)
	author := testutil.SeedUser(t, db, "manualtag")

	created, err := svc.Create(context.Background(), author.ID, "标题", "正文", []string{"手填"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tagger.calls != 0 {
		t.Fatalf("用户已填标签，模型仍被调用了 %d 次", tagger.calls)
	}
	if n := pendingTagJobs(t, rdb); n != 0 {
		t.Fatalf("手填标签时不该投任务，实际 %d 条", n)
	}
	if len(created.Tags) != 1 || created.Tags[0] != "手填" {
		t.Fatalf("标签 = %v，期望 [手填]", created.Tags)
	}
}

// TestTagWorkerFillsTagsForArticle 验证 worker 真的把模型建议落库。
//
// 这是异步链路的下半段：发布只投任务，标签最终能不能出现全靠这里。
// 要通过 Detail 回查，而不是看返回值——Detail 走的是数据库，
// 能证明关联表真写了一行，而不只是内存里塞了个字段。
func TestTagWorkerFillsTagsForArticle(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "后端开发"}
	svc := NewArticleService(db, rdb, tagger)
	worker := NewTagWorker(svc, rdb)
	author := testutil.SeedUser(t, db, "workerfill")

	created, err := svc.Create(ctx, author.ID, "MySQL 索引怎么建", "讲联合索引的最左前缀。", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 直接调 handle，绕过消费循环：这里要验的是处理逻辑，
	// 不是 XReadGroup 能不能读到消息（那是 event 包的测试范围）。
	if err := worker.handle(ctx, tagMessage(t, created.ID)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if tagger.calls != 1 {
		t.Fatalf("模型调用次数 = %d，期望 1", tagger.calls)
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "后端开发" {
		t.Fatalf("落库后的标签 = %v，期望 [后端开发]", got.Tags)
	}
}

// TestTagWorkerSkipsArticleWithTags 验证已经有标签的文章不再消耗模型额度。
//
// 重复投递在这个链路里是常态：多实例分摊、进程重启从 PEL 重放、
// 用户在异步窗口里自己补了标签。没有这道闸门，同一篇文章会被模型判好几次。
func TestTagWorkerSkipsArticleWithTags(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "自动补的"}
	svc := NewArticleService(db, rdb, tagger)
	worker := NewTagWorker(svc, rdb)
	author := testutil.SeedUser(t, db, "workerskip")

	created, err := svc.Create(ctx, author.ID, "标题", "正文", []string{"手填"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := worker.handle(ctx, tagMessage(t, created.ID)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if tagger.calls != 0 {
		t.Fatalf("已有标签仍调了模型 %d 次", tagger.calls)
	}
}

// TestTagWorkerIgnoresMissingArticle 验证文章被删后任务直接作废。
//
// 返回 error 会让这条消息留在 PEL 里每轮重试，直到有人手工处理。
// 文章已经没了，重试一万次也不会补出标签，只会把 PEL 撑大。
func TestTagWorkerIgnoresMissingArticle(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "不该出现"}
	svc := NewArticleService(db, rdb, tagger)
	worker := NewTagWorker(svc, rdb)
	author := testutil.SeedUser(t, db, "workermissing")

	created, err := svc.Create(ctx, author.ID, "标题", "正文", []string{"手填"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 软删除后 GORM 的默认作用域会把它过滤掉，worker 查不到就应该安静退出。
	if err := svc.Delete(ctx, created.ID, author.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := worker.handle(ctx, tagMessage(t, created.ID)); err != nil {
		t.Fatalf("文章已删时不该返回错误（会一直重试）: %v", err)
	}
	if tagger.calls != 0 {
		t.Fatalf("文章已删仍调了模型 %d 次", tagger.calls)
	}
}

// TestTagWorkerKeepsArticleBareWhenModelFails 验证模型失败只是「没标签」，不是「出错重试」。
// 密钥过期、额度耗尽这类问题重试也不会好，让它留在 PEL 里只会每轮失败一次。
// 同时文章必须还在、还没有标签，不能因为补标签失败而回滚发布。
func TestTagWorkerKeepsArticleBareWhenModelFails(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{err: errors.New("upstream timeout")}
	svc := NewArticleService(db, rdb, tagger)
	worker := NewTagWorker(svc, rdb)
	author := testutil.SeedUser(t, db, "workerfail")

	created, err := svc.Create(ctx, author.ID, "没有标签也要能发", "正文", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := worker.handle(ctx, tagMessage(t, created.ID)); err != nil {
		t.Fatalf("模型失败不该返回错误: %v", err)
	}
	if tagger.calls != 1 {
		t.Fatalf("模型调用次数 = %d，期望 1", tagger.calls)
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(got.Tags) != 0 {
		t.Fatalf("失败时不该有标签，实际 %v", got.Tags)
	}
}

// TestTagWorkerDoesNotOverwriteTagsAddedWhileThinking 验证模型思考期间用户补的标签不会被覆盖。
//
// 这是异步链路最容易漏的一个竞态：worker 先查「没标签」，再花几秒问模型，
// 这期间作者进编辑页自己填了标签并保存。没有事务内的二次确认，
// worker 会把用户亲手写的分类换成模型的猜测，而用户完全不知道发生了什么。
func TestTagWorkerDoesNotOverwriteTagsAddedWhileThinking(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "模型给的"}
	svc := NewArticleService(db, rdb, tagger)
	worker := NewTagWorker(svc, rdb)
	author := testutil.SeedUser(t, db, "workerrace")

	created, err := svc.Create(ctx, author.ID, "原标题", "原正文", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 在模型「思考」的那一刻插入一次作者编辑，模拟真实的时间重叠。
	tagger.onCall = func() {
		if _, err := svc.Update(ctx, created.ID, author.ID, "原标题", "原正文", []string{"作者补的"}); err != nil {
			t.Errorf("并发编辑失败: %v", err)
		}
	}

	if err := worker.handle(ctx, tagMessage(t, created.ID)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	got, err := svc.Detail(ctx, created.ID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "作者补的" {
		t.Fatalf("标签 = %v，期望保留作者手填的 [作者补的]", got.Tags)
	}
}

// TestUpdateNeverSuggestsTag 验证编辑接口不会补标签，也不会投任务。
//
// 编辑时把标签全删掉是用户的明确动作，自动补一个等于「删不掉」，
// 用户会反复删、反复出现，最后只能认为标签功能坏了。
func TestUpdateNeverSuggestsTag(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rdb := testutil.NewRedis(t)
	tagger := &fakeTagger{tag: "自动补的"}
	svc := NewArticleService(db, rdb, tagger)
	author := testutil.SeedUser(t, db, "updatetag")

	created, err := svc.Create(ctx, author.ID, "原标题", "原正文", []string{"手填"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := pendingTagJobs(t, rdb)

	updated, err := svc.Update(ctx, created.ID, author.ID, "新标题", "新正文", nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tagger.calls != 0 {
		t.Fatalf("编辑时模型被调用了 %d 次", tagger.calls)
	}
	if n := pendingTagJobs(t, rdb); n != before {
		t.Fatalf("编辑后任务数 %d → %d，编辑不该投任务", before, n)
	}
	if len(updated.Tags) != 0 {
		t.Fatalf("标签应当被清空，实际 %v", updated.Tags)
	}
}

// tagMessage 拼一条与 XAdd 形状一致的消息，省得每个用例都写一遍 Values。
// 值用 uint 而不是字符串，正是为了守住「worker 里 fmt.Sprint 兼容非字符串值」这件事：
// XAdd 传 any 时 go-redis 会把 uint 原样写进去，取出来仍是 int 类型。
func tagMessage(t *testing.T, articleID uint) redis.XMessage {
	t.Helper()
	return redis.XMessage{ID: "0-1", Values: map[string]any{"id": articleID}}
}

// postSuggest 发一次写作页标签建议请求，返回状态码与解析出的 tag。
// body 直接给 JSON 文本，用例里想传什么形状就传什么形状。
func postSuggest(t *testing.T, h *ArticleHandler, userID uint, body string) (int, string) {
	t.Helper()

	r := gin.New()
	r.POST("/api/articles/tag-suggest", asUser(userID), h.SuggestTag)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/articles/tag-suggest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	var env struct {
		Data tagSuggestResp `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v，原文 %s", err, w.Body.String())
	}
	return w.Code, env.Data.Tag
}

// TestSuggestTagEndpointReturnsTag 验证写作页拿到模型建议。
//
// 顺带守住「标题正文都空时不烧额度」：模型只会随手编一个词，
// 这次调用没有任何价值，却会占掉用户一小时里的一次配额。
func TestSuggestTagEndpointReturnsTag(t *testing.T) {
	db := testutil.NewDB(t)
	tagger := &fakeTagger{tag: "后端开发"}
	svc := NewArticleService(db, testutil.NewRedis(t), tagger)
	h := NewArticleHandler(svc, nil)

	code, tag := postSuggest(t, h, 7, `{"title":"MySQL 索引怎么建","content":"讲联合索引的最左前缀。"}`)
	if code != http.StatusOK || tag != "后端开发" {
		t.Fatalf("状态码 = %d，tag = %q，期望 200 与 后端开发", code, tag)
	}
	if tagger.calls != 1 {
		t.Fatalf("模型调用次数 = %d，期望 1", tagger.calls)
	}

	code, tag = postSuggest(t, h, 7, `{"title":"  ","content":""}`)
	if code != http.StatusOK || tag != "" {
		t.Fatalf("空内容应当返回空建议，实际 %d / %q", code, tag)
	}
	if tagger.calls != 1 {
		t.Fatalf("空内容不该调模型，调用次数变成 %d", tagger.calls)
	}
}

// TestSuggestTagEndpointEnforcesQuota 验证配额用完返回 429。
//
// 这个接口每调一次都真的打一次大模型（要花钱），而登录就能调。
// 没有上限的话，一个脚本就能把当天的额度刷干净。
func TestSuggestTagEndpointEnforcesQuota(t *testing.T) {
	db := testutil.NewDB(t)
	tagger := &fakeTagger{tag: "随便一个"}
	svc := NewArticleService(db, testutil.NewRedis(t), tagger)
	h := NewArticleHandler(svc, nil)

	body := `{"title":"标题","content":"正文内容够长就能问模型"}`
	// 配额是每小时 20 次，前 20 次通过。
	for i := 1; i <= tagSuggestPerHour; i++ {
		if code, _ := postSuggest(t, h, 9, body); code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态码 = %d，期望 200", i, code)
		}
	}
	code, _ := postSuggest(t, h, 9, body)
	if code != http.StatusTooManyRequests {
		t.Fatalf("超出配额状态码 = %d，期望 429", code)
	}

	// 配额按用户分桶：同 IP 的另一个用户不该被前一个人的用量牵连。
	if code, _ := postSuggest(t, h, 10, body); code != http.StatusOK {
		t.Fatalf("另一个用户状态码 = %d，期望 200", code)
	}
}
