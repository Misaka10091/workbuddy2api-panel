package pool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFlushFailureKeepsDirty 落盘失败必须保持 dirty。
//
// 旧实现是 `dirty.Swap(false)` 之后再写：写失败（磁盘满/权限丢）时脏标记已被清掉，
// 下一轮 flusher 看到 dirty=false 什么都不做——冷却、禁用、连续 12153 计数从此只留在
// 内存里，重启全丢。恢复这条语义后本测试必须失败。
//
// 失败用「父路径是普通文件」制造（比 chmod 稳定，见 backend/quality-and-testing.md）。
func TestFlushFailureKeepsDirty(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备阻塞文件: %v", err)
	}

	p := New(filepath.Join(blocker, "state.json")) // 父目录无法创建 → 落盘必失败
	defer p.Close()
	p.Add(&auth.Auth{UID: "u1"})

	p.mu.Lock()
	p.dirty.Store(true)
	p.flushLocked()
	dirty := p.dirty.Load()
	p.mu.Unlock()

	if !dirty {
		t.Fatal("落盘失败后 dirty 必须保持 true，否则状态再也不会被重试")
	}
	if _, err := os.Stat(filepath.Join(blocker, "state.json")); err == nil {
		t.Fatal("这台机器的路径布局意外允许写入，测试前提不成立")
	}
}

// TestLoadRecoversFromBackupWhenMainCorrupt 主文件损坏时从 .bak 恢复，
// 并把坏文件改名 .corrupt 保留证据（不然后续启动会再踩同一个坏文件）。
func TestLoadRecoversFromBackupWhenMainCorrupt(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")

	if err := os.WriteFile(fp, []byte(`{"accounts":`), 0o600); err != nil { // 截断的坏文件
		t.Fatalf("写坏文件: %v", err)
	}
	bak := `{"accounts":{"u1":{"disabled":true,"reason":"12153 session dead"}}}`
	if err := os.WriteFile(fp+".bak", []byte(bak), 0o600); err != nil {
		t.Fatalf("写备份: %v", err)
	}

	p := New(fp)
	defer p.Close()

	st, ok := p.Status("u1")
	if !ok {
		t.Fatalf("应从 .bak 恢复出账号 u1")
	}
	if !st.Disabled {
		t.Fatalf("恢复的禁用态丢失: %+v", st)
	}
	if _, err := os.Stat(fp + ".corrupt"); err != nil {
		t.Errorf("坏文件应改名 state.json.corrupt: %v", err)
	}
}

// TestLoadMissingMainKeepsEmptyPool 主文件不存在（首次启动）时保持空池，
// 不得因为「没有备份」而把 .corrupt 之类的东西误当状态读进来。
func TestLoadMissingMainKeepsEmptyPool(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")

	p := New(fp)
	defer p.Close()
	if n := len(p.List()); n != 0 {
		t.Fatalf("首次启动应为空池，得到 %d 个账号", n)
	}
}

// TestSaveWritesBackupOnSuccess 成功落盘时轮转出 .bak：
// 第二次落盘前主文件已是有效状态，它被覆盖前要留一份可用副本。
func TestSaveWritesBackupOnSuccess(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")

	p := New(fp)
	defer p.Close()
	p.Add(&auth.Auth{UID: "u1"})
	p.mu.Lock()
	p.dirty.Store(true)
	p.flushLocked()
	p.mu.Unlock()

	if _, err := os.Stat(fp); err != nil {
		t.Fatalf("主文件应已写出: %v", err)
	}

	// 第二次落盘：此时主文件存在，应轮转出 .bak。
	p.SetCredits("u1", 42, 0)
	p.mu.Lock()
	p.dirty.Store(true)
	p.flushLocked()
	p.mu.Unlock()

	if _, err := os.Stat(fp + ".bak"); err != nil {
		t.Fatalf("第二次落盘应轮转出 state.json.bak: %v", err)
	}
}
