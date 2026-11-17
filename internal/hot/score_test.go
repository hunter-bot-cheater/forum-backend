package hot

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"

	"forum-backend/internal/keys"
	"forum-backend/internal/testutil"
)

// TestScoreOf 钉住权重本身。
//
// 这个函数是四处口径的共同来源（重建、点赞、浏览、对账），
// 数值一变，全站排名同时变。所以断言写死期望值，不用 ScoreOf 自己算期望。
func TestScoreOf(t *testing.T) {
	cases := []struct {
		name      string
		likeCount int64
		viewCount int64
		want      int64
	}{
		{name: "没赞没浏览", likeCount: 0, viewCount: 0, want: 0},
		{name: "只有浏览", likeCount: 0, viewCount: 1, want: 1},
		{name: "只有赞", likeCount: 1, viewCount: 0, want: 3},
		{name: "两者都有", likeCount: 2, viewCount: 100, want: 106},
		{name: "大量浏览压过少量赞", likeCount: 1, viewCount: 50, want: 53},
	}
	for _, c := range cases {
		if got := ScoreOf(c.likeCount, c.viewCount); got != c.want {
			t.Fatalf("%s: ScoreOf(%d, %d) = %d, want %d",
				c.name, c.likeCount, c.viewCount, got, c.want)
		}
	}
}

// TestAddViewScoreOnlyBumpsMembers 验证浏览只推进已在榜文章的分数。
//
// 为什么不能给不在榜的文章建成员：每篇被打开过的文章都进 ZSET，
// 几年后这个集合就是全站文章的镜像，而榜单只需要最热的那几百篇。
// 这条用例同时钉住「不回错」：成员不存在时 Redis 返回空，那不是异常。
func TestAddViewScoreOnlyBumpsMembers(t *testing.T) {
	rdb := testutil.NewRedis(t)
	ctx := context.Background()

	onBoard := "11"
	if err := rdb.ZAdd(ctx, keys.HotArticlesKey, redis.Z{Score: 10, Member: onBoard}).Err(); err != nil {
		t.Fatalf("预置榜单成员失败: %v", err)
	}

	if err := AddViewScore(ctx, rdb, 11); err != nil {
		t.Fatalf("在榜文章加浏览权重失败: %v", err)
	}
	score, err := rdb.ZScore(ctx, keys.HotArticlesKey, onBoard).Result()
	if err != nil {
		t.Fatalf("读分数失败: %v", err)
	}
	if score != 11 {
		t.Errorf("在榜文章分数 = %v，期望 11（10 + 一次浏览）", score)
	}

	// 不在榜的文章：调用要成功返回，且不能凭空多出一个成员。
	if err := AddViewScore(ctx, rdb, 12); err != nil {
		t.Fatalf("不在榜文章加浏览权重应当静默跳过，实际报错: %v", err)
	}
	_, err = rdb.ZScore(ctx, keys.HotArticlesKey, "12").Result()
	if !errors.Is(err, redis.Nil) {
		t.Errorf("不在榜的文章不该被加进 ZSET，实际 err = %v", err)
	}
}
