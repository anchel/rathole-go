package common

import (
	"testing"
)

type fakeRunnable struct {
	name   string
	closed bool
}

func (f *fakeRunnable) Close() { f.closed = true }
func (f *fakeRunnable) Run()   {}

// 客户端重连时，新通道会用同一个 key 注册进来。
// 旧通道退出时若按 key 摘除，删掉的正是刚建好的新通道，
// 表现为「重连第一次总是被静默掐断」。
func TestRemoveItemDoesNotDropReplacement(t *testing.T) {
	rm := NewRunnableManager()

	oldCC := &fakeRunnable{name: "old"}
	newCC := &fakeRunnable{name: "new"}

	rm.Put("digest-a", "session-1", oldCC)

	// 客户端重连：新通道顶掉旧通道
	rm.Put("digest-a", "session-2", newCC)
	if !oldCC.closed {
		t.Fatal("旧通道应当被 Put 关闭")
	}

	// 旧通道的收尾清理姗姗来迟，不能影响新通道
	rm.RemoveItem(oldCC)

	got := rm.GetByKey1("digest-a")
	if got == nil {
		t.Fatal("新通道被旧通道的收尾清理误删了")
	}
	if got != Runnable(newCC) {
		t.Fatalf("取到的不是新通道, got %v", got)
	}
	if newCC.closed {
		t.Fatal("新通道不应被关闭")
	}
}

func TestRemoveItemRemovesItself(t *testing.T) {
	rm := NewRunnableManager()
	cc := &fakeRunnable{name: "solo"}

	rm.Put("digest-a", "session-1", cc)
	rm.RemoveItem(cc)

	if rm.Len() != 0 {
		t.Fatalf("条目应当被摘除, len=%d", rm.Len())
	}
}

// 空 key 不能匹配到任何条目，否则会捞出不相干的对象。
func TestEmptyKeyNeverMatches(t *testing.T) {
	rm := NewRunnableManager()
	rm.Put("digest-a", "session-1", &fakeRunnable{name: "a"})

	if got := rm.GetByKey1(""); got != nil {
		t.Fatal("空 key1 不应匹配到条目")
	}
	if got := rm.GetByKey2(""); got != nil {
		t.Fatal("空 key2 不应匹配到条目")
	}
}

func TestGetByKey2(t *testing.T) {
	rm := NewRunnableManager()
	cc := &fakeRunnable{name: "a"}
	rm.Put("digest-a", "session-1", cc)

	if got := rm.GetByKey2("session-1"); got != Runnable(cc) {
		t.Fatal("按 session_key 应当能取到对应的通道")
	}
	if got := rm.GetByKey2("session-x"); got != nil {
		t.Fatal("不存在的 session_key 不应匹配到条目")
	}
}
