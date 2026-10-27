// sessiondead_dirty_test.go 12153 连续计数的落盘护栏（0ebd6e7 同口径）。
//
// 计数只在内存里涨、重启归零，会让「离禁用还差几次」重新从零学；清零点不落盘更糟：
// 刚被 refresh 证明存活的账号重启后旧计数复活，下次失败直接被推到阈值。
package pool

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestSessionDeadMidCountMarksDirty(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.dirty.Store(false)

	if p.NoteSessionDead("u1") {
		t.Fatal("阈值内不应禁用")
	}
	if !p.dirty.Load() {
		t.Error("阈值内 +1 未置脏：重启会丢进度")
	}
}

func TestClearSessionDeadMarksDirty(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteSessionDead("u1") // 计数 1
	p.dirty.Store(false)

	p.ClearSessionDead("u1")
	if !p.dirty.Load() {
		t.Error("清零未置脏：重启会复活旧计数并误杀")
	}
}

func TestClearSessionDeadNoopWhenZero(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.dirty.Store(false)

	p.ClearSessionDead("u1") // 计数本就是 0
	if p.dirty.Load() {
		t.Error("零值清零不该置脏（无状态变化）")
	}
}
