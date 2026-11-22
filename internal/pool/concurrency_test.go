package pool

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"forum-backend/internal/pool/sim"
)

// 50 局同时开局、同时出杆、同时有人观战。
//
// 这条用例守的是「一局一个 actor」这个设计本身：
// 每局的状态只在自己那条协程里读写，Manager 只保护那张 map。
// 跑 -race 时如果哪一处把状态漏到了协程外面，这里会直接报出来。
//
// 用假仿真：真 pooltool 单杆要一秒多，50 局 × 5 杆会让这条用例跑上几分钟。
func TestManyMatchesRunConcurrently(t *testing.T) {
	const matches = 50
	const shotsPerMatch = 5

	manager, err := NewManager(Options{Sim: &fakeSim{shot: quietShot()}, TurnTimeout: time.Minute})
	if err != nil {
		t.Fatalf("构造 manager 失败：%v", err)
	}
	t.Cleanup(manager.CloseAll)

	// 每局两名玩家 + 两名观战者，同时上线。
	var wait sync.WaitGroup
	errs := make(chan error, matches)

	for index := 0; index < matches; index += 1 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()

			matchID := uint(1000 + index)
			state := newMatchState(matchID, fmt.Sprintf("%06d", matchID), [2]PlayerBrief{
				{UserID: uint(2*index + 1), Nickname: "甲"},
				{UserID: uint(2*index + 2), Nickname: "乙"},
			})
			match := manager.Create(state)

			conn0 := &fakeConn{}
			conn1 := &fakeConn{}
			if err := match.Join(Seat0, conn0); err != nil {
				errs <- fmt.Errorf("对局 %d 座位 0 进房：%w", matchID, err)
				return
			}
			if err := match.Join(Seat1, conn1); err != nil {
				errs <- fmt.Errorf("对局 %d 座位 1 进房：%w", matchID, err)
				return
			}
			for watcher := 0; watcher < 2; watcher += 1 {
				if err := match.Watch(&fakeConn{}); err != nil {
					errs <- fmt.Errorf("对局 %d 观战者进房：%w", matchID, err)
					return
				}
			}

			// 轮流出杆。第一杆是开球：假轨迹里只有「母球碰 9 号、9 号碰库」，
			// 不满足开球的四库/进球条件，会被判犯规并把手中球交给对手，
			// 所以每次出杆前都按快照补一次落位。
			for shot := 0; shot < shotsPerMatch; shot += 1 {
				seat := Seat(shot % 2)
				snapshot, err := match.Snapshot(seat)
				if err != nil {
					errs <- fmt.Errorf("对局 %d 取快照：%w", matchID, err)
					return
				}
				if snapshot.BallInHand {
					if err := match.PlaceCue(seat, 0.5, 0.6); err != nil {
						errs <- fmt.Errorf("对局 %d 第 %d 杆落位：%w", matchID, shot+1, err)
						return
					}
				}
				if err := match.Shoot(seat, sim.CueParams{Phi: 1.5, Power: 0.6}, nil, shot); err != nil {
					errs <- fmt.Errorf("对局 %d 第 %d 杆：%w", matchID, shot+1, err)
					return
				}
			}

			snapshot, err := match.Snapshot(SeatSpectator)
			if err != nil {
				errs <- fmt.Errorf("对局 %d 取快照：%w", matchID, err)
				return
			}
			if snapshot.ShotSeq != shotsPerMatch {
				errs <- fmt.Errorf("对局 %d 的杆数 = %d，期望 %d", matchID, snapshot.ShotSeq, shotsPerMatch)
			}
			if match.SpectatorCount() != 2 {
				errs <- fmt.Errorf("对局 %d 的观战人数 = %d，期望 2", matchID, match.SpectatorCount())
			}
		}(index)
	}

	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if manager.Count() != matches {
		t.Errorf("内存里的对局数 = %d，期望 %d", manager.Count(), matches)
	}
}

// 观战名单的读写不会互踩：一边有人进出，一边有人在读人数。
//
// SpectatorCount 走的是原子读，没加锁也不会撕裂；
// 这条用例在 -race 下才真正有意义。
func TestSpectatorCountWhileJoiningAndLeaving(t *testing.T) {
	match, _, _ := newTestMatch(t, &fakeSim{shot: quietShot()})

	var wait sync.WaitGroup
	for index := 0; index < 40; index += 1 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			conn := &fakeConn{}
			if err := match.Watch(conn); err != nil {
				// 名额满了是正常结果，不是失败。
				return
			}
			_ = match.SpectatorCount()
			_ = match.NotifyDisconnect(SeatSpectator, conn)
		}(index)
	}
	wait.Wait()

	if count := match.SpectatorCount(); count != 0 {
		t.Errorf("全部断开后观战人数 = %d，期望 0", count)
	}
}
