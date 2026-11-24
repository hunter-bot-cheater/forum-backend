package pool

import (
	"strings"
	"testing"

	"forum-backend/internal/pool/sim"
)

// solidIDs 是八球里的七个全色球。
//
// 不能用字符串比较判断「1 到 7」：按字典序 "10" 排在 "7" 前面，
// 会把花色球当成全色球。
var solidIDs = map[string]bool{
	"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true,
}

// TestRackBallsFollowRules 检查每局重排后的球堆仍然符合八球的摆球规则。
//
// 球号是洗过的，所以只能验性质，不能逐个对坐标：号码不重不漏、8 号在中心、
// 最后一排两个角一实一花、母球没被洗走。
func TestRackBallsFollowRules(t *testing.T) {
	balls := rackBalls()
	if len(balls) != len(StandardRack) {
		t.Fatalf("球数 = %d，期望 %d", len(balls), len(StandardRack))
	}

	seen := make(map[string]bool, len(balls))
	for _, ball := range balls {
		if seen[ball.ID] {
			t.Fatalf("%s 号球出现了两次", ball.ID)
		}
		seen[ball.ID] = true
		if !ball.OnTable {
			t.Fatalf("%s 号球开局就在袋里", ball.ID)
		}
	}
	for _, id := range []string{
		sim.CueBallID, "1", "2", "3", "4", "5", "6", "7", "8", "9",
		"10", "11", "12", "13", "14", "15",
	} {
		if !seen[id] {
			t.Fatalf("缺少 %s 号球", id)
		}
	}

	// 8 号必须在三角形中心：槽位是固定的，只有球号在洗。
	center := StandardRack[eightBallSlot+1] // +1：第一项是母球
	if got := ballAtSlot(balls, center); got != sim.EightBallID {
		t.Fatalf("三角形中心是 %s 号，期望 8 号", got)
	}
	// 母球也要待在自己那个槽位上，不能被当成普通球洗出去。
	cue := StandardRack[0]
	if got := ballAtSlot(balls, cue); got != sim.CueBallID {
		t.Fatalf("母球槽位上摆着 %s 号", got)
	}

	left := ballAtSlot(balls, StandardRack[lastRowLeftSlot+1])
	right := ballAtSlot(balls, StandardRack[lastRowRightSlot+1])
	if solidIDs[left] == solidIDs[right] {
		t.Fatalf("底排两角是 %s / %s，应该一实一花", left, right)
	}
}

// TestRackBallsShuffleBetweenMatches 检查每一局摆出来的球堆不完全一样。
//
// 坐标固定加确定性物理，会让同样的球堆散出同样的结果——玩家看到的就是
// 「几乎每一把都是绿球进右下底袋」。这条用例守的是「球号真的在洗」。
func TestRackBallsShuffleBetweenMatches(t *testing.T) {
	first := rackSignature(rackBalls())
	for i := 0; i < 20; i++ {
		if rackSignature(rackBalls()) != first {
			return
		}
	}
	t.Fatal("连摆 21 局球号都没变，开球结果会一直重复")
}

// ballAtSlot 返回某个槽位坐标上摆着的球号，没有就返回空串。
func ballAtSlot(balls []sim.ResultBall, slot RackBall) string {
	for _, ball := range balls {
		if ball.X == slot.X && ball.Y == slot.Y {
			return ball.ID
		}
	}
	return ""
}

// rackSignature 把一局球堆按点位连成字符串，用来比较两局是不是同一张球堆。
func rackSignature(balls []sim.ResultBall) string {
	var builder strings.Builder
	for _, ball := range balls {
		builder.WriteString(ball.ID)
		builder.WriteByte(',')
	}
	return builder.String()
}
