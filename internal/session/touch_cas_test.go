// touch_cas_test.go 粘性绑定 touch 的 CAS 语义护栏（2026-10 竞态修复）。
package session

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/redisstore"
)

// TestTouchDoesNotOverwriteNewerBinding 迟到的 touch 不能把已改绑的会话冲回旧号。
func TestTouchDoesNotOverwriteNewerBinding(t *testing.T) {
	r := routerWith(redisstore.Noop{}, []string{"u1", "u2"}, time.Minute)
	now := time.Now()
	r.mu.Lock()
	r.entries["k"] = entry{uid: "u2", lastActive: now}
	r.mu.Unlock()

	// 慢请求带着旧 uid 回来 touch。
	r.touch("k", "u1", now.Add(time.Second))

	r.mu.RLock()
	got := r.entries["k"].uid
	r.mu.RUnlock()
	if got != "u2" {
		t.Errorf("touch 覆盖了新绑定: uid=%q want u2", got)
	}
}

// TestTouchDoesNotReviveUnboundKey Unbind 之后的迟到 touch 不能把绑定「复活」。
func TestTouchDoesNotReviveUnboundKey(t *testing.T) {
	r := routerWith(redisstore.Noop{}, []string{"u1"}, time.Minute)
	now := time.Now()
	r.mu.Lock()
	r.entries["k"] = entry{uid: "u1", lastActive: now}
	r.mu.Unlock()
	r.Unbind("k")

	r.touch("k", "u1", now)

	r.mu.RLock()
	_, ok := r.entries["k"]
	r.mu.RUnlock()
	if ok {
		t.Error("touch 复活了已解绑的会话键")
	}
}

// TestTouchRollsActivityForSameUID 绑定未变时正常滚动时间戳（不误伤正常路径）。
func TestTouchRollsActivityForSameUID(t *testing.T) {
	r := routerWith(redisstore.Noop{}, []string{"u1"}, time.Minute)
	now := time.Now()
	r.mu.Lock()
	r.entries["k"] = entry{uid: "u1", lastActive: now}
	r.mu.Unlock()

	later := now.Add(time.Second)
	r.touch("k", "u1", later)

	r.mu.RLock()
	got := r.entries["k"].lastActive
	r.mu.RUnlock()
	if !got.Equal(later) {
		t.Errorf("lastActive=%v want %v", got, later)
	}
}
