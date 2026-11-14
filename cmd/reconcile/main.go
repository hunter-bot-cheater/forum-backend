// Command reconcile 对账热榜投影与消息链路。
//
// 默认只报告不改数据，加 -fix 才动手。退出码约定：
//
//	0  一致，或修正后已收敛
//	1  发现漂移 / 僵尸成员 / 死信
//	2  运行失败（连不上库、查询报错）
//
// 这样可以直接挂到计划任务或 CI 里，靠退出码判断要不要人来看。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"forum-backend/internal/config"
	"forum-backend/internal/hot"
)

func main() {
	os.Exit(run())
}

func run() int {
	topN := flag.Int("top", 200, "对账检查的榜单深度")
	fix := flag.Bool("fix", false, "修正发现的漂移。默认只报告，不写 Redis")
	timeout := flag.Duration("timeout", 30*time.Second, "整体超时")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Printf("加载配置失败: %v", err)
		return 2
	}

	db, err := config.InitDB(&cfg.MySQL)
	if err != nil {
		log.Printf("连接数据库失败: %v", err)
		return 2
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}

	rdb, err := config.InitRedis(&cfg.Redis)
	if err != nil {
		log.Printf("连接 Redis 失败: %v", err)
		return 2
	}
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	rec := hot.NewReconciler(db, rdb)

	rep, err := rec.Check(ctx, *topN)
	if err != nil {
		log.Printf("对账失败: %v", err)
		return 2
	}

	// 只报告时先打印再退出，让人看清修之前长什么样。
	if !*fix {
		printReport(rep)
		if rep.HasProblem() {
			return 1
		}
		return 0
	}

	// 开修时先动手、后打印。Check 到 Fix 之间是覆盖窗口：
	// 这期间有人点赞，写回的就是旧计数。打印 JSON 要几毫秒到几百毫秒，
	// 把它挪到后面，窗口缩到只剩两条 Redis 命令的间隔。
	// 报告本身记录的是修正前的快照，打印出来仍然能对照修了什么。
	fixed, err := rec.Fix(ctx, rep)
	if err != nil {
		log.Printf("修正失败: %v（已修 %d 个）", err, fixed)
		return 2
	}

	printReport(rep)
	fmt.Printf("fixed=%d\n", fixed)

	// 修完再跑一次。对账的价值在于收敛：第二次必须干净，
	// 否则说明修正逻辑本身有问题，或者有并发写入在持续制造漂移。
	again, err := rec.Check(ctx, *topN)
	if err != nil {
		log.Printf("修正后复查失败: %v", err)
		return 2
	}
	fmt.Println("after fix: " + again.Summary())
	if again.HasProblem() {
		return 1
	}
	return 0
}

func printReport(rep *hot.ReconcileReport) {
	pretty, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		log.Printf("序列化报告失败: %v", err)
		return
	}
	fmt.Println(string(pretty))
	fmt.Println(rep.Summary())
}
