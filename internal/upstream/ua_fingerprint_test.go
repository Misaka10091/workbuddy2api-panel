package upstream

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 出站 UA 形态（CN 5.9.3 / global 5.8.4，内置 CLI 2.165.0）。
//
// chat/refresh/FetchModels 走 userAgent：首段恒驼峰 `WorkBuddy`，第二段品牌随 realm。
// 桌面端（report / user-asset / 专家召唤）走 Client.desktopUAFor：两段品牌一致。
//
// 2026-10-28 版本段反转（上游 47f1ed6，CN 5.9.3 真机复核）：CN 从
// `WorkBuddy/5.8.3 … CLI/2.161.0` 变 `WorkBuddy/5.9.3 … CLI/2.165.0`；global 的
// **客户端段仍是 5.8.4**（无 5.9.3 的 global 抓包，不臆造），但 CLI 段是两域共用
// 单常量，跟着变成 2.165.0——这是本批唯一波及 global 的改动。
func TestUARealmBrand(t *testing.T) {
	c := New()
	global := &auth.Auth{UID: "u1", Domain: "www.workbuddy.ai"}
	global.BackfillRealm()
	cn := &auth.Auth{UID: "u2", Domain: "copilot.tencent.com"}
	cn.BackfillRealm()

	if got, want := c.userAgent(cn), "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0"; got != want {
		t.Errorf("CN chat UA = %q want %q", got, want)
	}
	if got, want := c.userAgent(global), "WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.165.0"; got != want {
		t.Errorf("global chat UA = %q want %q", got, want)
	}
	if got, want := c.desktopUAFor(cn), "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0"; got != want {
		t.Errorf("CN desktop UA = %q want %q", got, want)
	}
	if got, want := c.desktopUAFor(global), "workbuddy-ai/5.8.4 workbuddy-ai/5.8.4 CLI/2.165.0"; got != want {
		t.Errorf("global desktop UA = %q want %q", got, want)
	}
}

// TestUAOverrideStillWins 显式 client_version / user_agent 覆盖仍优先生效
// （既有逃生开关不能被 realm 默认值吃掉）。
func TestUAOverrideStillWins(t *testing.T) {
	global := &auth.Auth{UID: "u1", Domain: "www.workbuddy.ai"}
	global.BackfillRealm()
	c := &Client{ClientVersion: "9.9.9", CliVersion: "1.2.3"}
	if got, want := c.userAgent(global), "WorkBuddy/9.9.9 workbuddy-ai/9.9.9 CLI/1.2.3"; got != want {
		t.Errorf("override UA = %q want %q", got, want)
	}
	c.UserAgent = "MyAgent/1"
	if got := c.userAgent(global); got != "MyAgent/1" {
		t.Errorf("显式 user_agent 应完全覆盖，得到 %q", got)
	}
}

// TestIDEVersionsMatchUA X-IDE-Version 与上报体版本必须与 UA 同源：
// 版本段彼此不一致本身就是可判别的指纹。
func TestIDEVersionsMatchUA(t *testing.T) {
	global := &auth.Auth{UID: "u1", Domain: "www.workbuddy.ai", Nickname: "n"}
	global.BackfillRealm()
	cn := &auth.Auth{UID: "u2", Domain: "copilot.tencent.com", Nickname: "n"}
	cn.BackfillRealm()

	if ver, _, _ := desktopTriple(global); ver != defaultClientVersionGlobal {
		t.Errorf("global 桌面端版本=%q want %q", ver, defaultClientVersionGlobal)
	}
	if ver, _, _ := desktopTriple(cn); ver != defaultClientVersionCN {
		t.Errorf("CN 桌面端版本=%q want %q", ver, defaultClientVersionCN)
	}
	fp := desktopFingerprint(global)
	if fp["ideVersion"] != defaultClientVersionGlobal || fp["extVersion"] != defaultClientVersionGlobal {
		t.Errorf("global 上报体版本应与 UA 同源: %v/%v", fp["ideVersion"], fp["extVersion"])
	}
	if fp["commit"] != desktopCommitGlobal {
		t.Errorf("global commit=%v want %q", fp["commit"], desktopCommitGlobal)
	}
}
