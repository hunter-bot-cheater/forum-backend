package floor

import (
	"errors"
	"testing"
	"time"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
)

// 这些测试只覆盖不碰 IO 的纯函数。Grab 的并发正确性依赖
// Redis INCR 与 floor_prizes 的条件 UPDATE，需要真实数据库才能验，
// 手工压测步骤与结果写在 04f 文档第 7 节。

func TestParseLuckyFloors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []int
		wantErr bool
	}{
		{name: "normal", raw: "8,88,888", want: []int{8, 88, 888}},
		{name: "spaces trimmed", raw: " 8 , 88 ", want: []int{8, 88}},
		{name: "single", raw: "1", want: []int{1}},
		{name: "keep duplicates", raw: "8,8", want: []int{8, 8}},
		{name: "empty", raw: "", wantErr: true},
		{name: "blank", raw: "   ", wantErr: true},
		{name: "empty segment", raw: "8,,88", wantErr: true},
		{name: "non numeric", raw: "8,abc", wantErr: true},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "over limit", raw: "1000001", wantErr: true},
	}

	for _, c := range cases {
		got, err := parseLuckyFloors(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: parseLuckyFloors(%q) expected error, got %v", c.name, c.raw, got)
			} else if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("%s: error = %v, want ErrInvalidInput", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := 0; i < len(got); i++ {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestJoinLuckyFloors(t *testing.T) {
	cases := []struct {
		name    string
		in      []int
		want    string
		wantErr bool
	}{
		{name: "sorted and deduped", in: []int{88, 8, 888, 8}, want: "8,88,888"},
		{name: "single", in: []int{1}, want: "1"},
		{name: "empty", in: []int{}, wantErr: true},
		{name: "nil", in: nil, wantErr: true},
		{name: "zero", in: []int{0}, wantErr: true},
		{name: "negative", in: []int{-3}, wantErr: true},
		{name: "over limit", in: []int{maxLuckyFloor + 1}, wantErr: true},
	}

	for _, c := range cases {
		got, err := joinLuckyFloors(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got %q", c.name, got)
			} else if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("%s: error = %v, want ErrInvalidInput", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestJoinParseRoundTrip(t *testing.T) {
	in := []int{888, 8, 88, 8, 100}
	text, err := joinLuckyFloors(in)
	if err != nil {
		t.Fatalf("joinLuckyFloors: %v", err)
	}
	if text != "8,88,100,888" {
		t.Fatalf("normalized = %q, want %q", text, "8,88,100,888")
	}
	got, err := parseLuckyFloors(text)
	if err != nil {
		t.Fatalf("parseLuckyFloors: %v", err)
	}
	want := []int{8, 88, 100, 888}
	if len(got) != len(want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
	for i := 0; i < len(got); i++ {
		if got[i] != want[i] {
			t.Fatalf("round trip = %v, want %v", got, want)
		}
	}
}

func TestJoinLuckyFloorsTooMany(t *testing.T) {
	in := make([]int, 0, maxLuckyCount+1)
	for i := 1; i <= maxLuckyCount+1; i++ {
		in = append(in, i)
	}
	if _, err := joinLuckyFloors(in); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

func TestContainsFloor(t *testing.T) {
	floors := []int{8, 88, 888}
	if !containsFloor(floors, 88) {
		t.Error("88 should be a lucky floor")
	}
	if containsFloor(floors, 7) {
		t.Error("7 should not be a lucky floor")
	}
	if containsFloor(nil, 8) {
		t.Error("nil list should contain nothing")
	}
}

func TestCheckWindow(t *testing.T) {
	start := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)

	cases := []struct {
		name    string
		now     time.Time
		wantErr bool
	}{
		{name: "before start", now: start.Add(-time.Second), wantErr: true},
		{name: "exactly start", now: start, wantErr: false},
		{name: "in progress", now: start.Add(time.Hour), wantErr: false},
		{name: "exactly end", now: end, wantErr: true},
		{name: "after end", now: end.Add(time.Second), wantErr: true},
	}

	for _, c := range cases {
		err := checkWindow(c.now, start, end)
		if c.wantErr {
			if !errors.Is(err, domain.ErrFloorNotActive) {
				t.Errorf("%s: error = %v, want ErrFloorNotActive", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
	}
}

func TestJudgePrizeUpdate(t *testing.T) {
	if err := judgePrizeUpdate(1); err != nil {
		t.Fatalf("rows=1 should succeed, got %v", err)
	}
	if err := judgePrizeUpdate(0); !errors.Is(err, domain.ErrFloorTaken) {
		t.Fatalf("rows=0 error = %v, want ErrFloorTaken", err)
	}
	if err := judgePrizeUpdate(2); err == nil {
		t.Fatal("rows=2 should be treated as an anomaly")
	}
	if err := judgePrizeUpdate(2); errors.Is(err, domain.ErrFloorTaken) {
		t.Fatal("rows=2 should not be reported as already taken")
	}
}

func TestToWinnerRespUnclaimed(t *testing.T) {
	p := &model.FloorPrize{EventID: 1, Floor: 8}
	resp := toWinnerResp(p)
	if resp.Floor != 8 {
		t.Fatalf("floor = %d, want 8", resp.Floor)
	}
	if resp.UserID != 0 {
		t.Fatalf("user_id = %d, want 0", resp.UserID)
	}
	if !resp.CreatedAt.IsZero() {
		t.Fatalf("created_at = %v, want zero time", resp.CreatedAt)
	}
}
