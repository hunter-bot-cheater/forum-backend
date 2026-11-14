package event

import (
	"encoding/json"
	"testing"
)

// 评论事件同时带 Target 与 Created，编解码之后两者都要保持原样。
func TestPayloadRoundTrip(t *testing.T) {
	created := Target{Type: TargetComment, ID: 34}
	want, err := NewPayload(TypeCommentCreated, 5, Target{Type: TargetArticle, ID: 7}, &created)
	if err != nil {
		t.Fatalf("new payload: %v", err)
	}
	raw, err := want.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EventID != want.EventID || got.Type != want.Type || got.ActorID != want.ActorID {
		t.Fatalf("basic fields changed: got %+v", got)
	}
	if got.Target.Type != TargetArticle || got.Target.ID != 7 {
		t.Fatalf("target = %+v, want article/7", got.Target)
	}
	if got.Created == nil || got.Created.Type != TargetComment || got.Created.ID != 34 {
		t.Fatalf("created = %+v, want comment/34", got.Created)
	}
}

// 点赞事件没有新建实体，JSON 里只应该有固定的五个字段。
func TestLikePayloadIsLean(t *testing.T) {
	p, err := NewPayload(TypeLikeCreated, 5, Target{Type: TargetArticle, ID: 7}, nil)
	if err != nil {
		t.Fatalf("new payload: %v", err)
	}
	raw, err := p.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{"event_id", "type", "actor_id", "target", "occurred_at"}
	if len(fields) != len(wantKeys) {
		t.Fatalf("field count = %d, want %d: %s", len(fields), len(wantKeys), raw)
	}
	for _, key := range wantKeys {
		if _, ok := fields[key]; !ok {
			t.Errorf("field %s should be present: %s", key, raw)
		}
	}
}

// 评论多一个 created，其余字段与点赞一致。
func TestCommentPayloadCarriesCreated(t *testing.T) {
	created := Target{Type: TargetComment, ID: 34}
	p, err := NewPayload(TypeCommentCreated, 5, Target{Type: TargetArticle, ID: 7}, &created)
	if err != nil {
		t.Fatalf("new payload: %v", err)
	}
	raw, err := p.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 6 {
		t.Fatalf("field count = %d, want 6: %s", len(fields), raw)
	}
	if _, ok := fields["created"]; !ok {
		t.Errorf("created should be present: %s", raw)
	}
}
