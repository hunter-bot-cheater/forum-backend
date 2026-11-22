package realtime

import "testing"

// 这个文件只覆盖「谁在线」这两个查询。它们是台球邀请入口的判据：
// 判错一次就是「点了邀请对方根本收不到」，而界面上会显示成功。

// 没连接的、有一条的、同一个人开两个标签页的，都要答对。
func TestIsOnlineTracksConnections(t *testing.T) {
	hub := NewHub()

	if hub.IsOnline(7) {
		t.Fatal("还没注册就报在线")
	}
	if ids := hub.OnlineUserIDs(); len(ids) != 0 {
		t.Fatalf("空 Hub 的在线名单有 %d 个：%v", len(ids), ids)
	}

	first := &Client{userID: 7}
	second := &Client{userID: 7}
	hub.Register(first)
	hub.Register(second)

	if !hub.IsOnline(7) {
		t.Fatal("有两个连接的用户报离线")
	}
	// 同一用户的两条连接只能算一个人：在线名单是给人挑的，不是给连接挑的。
	if ids := hub.OnlineUserIDs(); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("在线名单 = %v，期望只有 7 一次", ids)
	}

	// 关掉其中一条，人还在线。
	hub.Unregister(first)
	if !hub.IsOnline(7) {
		t.Fatal("还有一条连接时被当成离线")
	}

	// 最后一条也断了：离线，而且名单里不留空壳。
	hub.Unregister(second)
	if hub.IsOnline(7) {
		t.Fatal("连接全断了还报在线")
	}
	if ids := hub.OnlineUserIDs(); len(ids) != 0 {
		t.Fatalf("断线后名单还留着：%v", ids)
	}
}

// 多个人同时在线时，名单要把每个人都带上，不能因为内层 map 共用而漏。
func TestOnlineUserIDsListsEveryone(t *testing.T) {
	hub := NewHub()
	for _, userID := range []uint{3, 5, 9} {
		hub.Register(&Client{userID: userID})
	}

	ids := hub.OnlineUserIDs()
	if len(ids) != 3 {
		t.Fatalf("名单 = %v，期望 3 个人", ids)
	}

	seen := make(map[uint]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, want := range []uint{3, 5, 9} {
		if !seen[want] {
			t.Errorf("名单里少了 %d：%v", want, ids)
		}
	}
}

// 名单返回的是拷贝：调用方排序、截断的时候不能动到登记表本身。
func TestOnlineUserIDsReturnsCopy(t *testing.T) {
	hub := NewHub()
	hub.Register(&Client{userID: 4})
	hub.Register(&Client{userID: 6})

	ids := hub.OnlineUserIDs()
	ids[0] = 999

	if hub.IsOnline(999) {
		t.Fatal("改动返回的切片影响到了登记表")
	}
}
