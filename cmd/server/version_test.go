package main

import (
	"regexp"
	"runtime/debug"
	"testing"
	"time"
)

// snapshotRe 无 tag 构建的版本串形状：v<+08:00 的 YYYY.MM.DD.HHMM>-<7 位 hash>。
// 这条形状是面板左上角与发布资产名的契约（README/CI 断言都靠它），不能随手改。
var snapshotRe = regexp.MustCompile(`^v\d{4}\.\d{2}\.\d{2}\.\d{4}-[0-9a-f]{7}$`)

// buildInfoOf 造一份只带指定 VCS settings 的构建信息。
func buildInfoOf(kv ...string) *debug.BuildInfo {
	bi := &debug.BuildInfo{}
	for i := 0; i+1 < len(kv); i += 2 {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
	}
	return bi
}

func TestResolveVersionTagWins(t *testing.T) {
	// 打 tag 构建：注入的 tag 原样展示，不再拼时间/hash，也不补 v 前缀
	// （CI 传的就是 v2026.10.27.2038，前端再补一个 v 会变成 vv…）。
	bi := buildInfoOf("vcs.revision", "0123456789abcdef0123456789abcdef01234567")
	got := resolveVersion(buildStamp{version: "v2026.10.27.2038"}, bi, time.Now())
	if got != "v2026.10.27.2038" {
		t.Fatalf("tag build version = %q, want tag verbatim", got)
	}

	// 前后空白/空串：空白算没注入，空串不算 tag。
	if got := resolveVersion(buildStamp{version: "  v1.14.0  "}, bi, time.Now()); got != "v1.14.0" {
		t.Fatalf("padded tag = %q, want trimmed", got)
	}
	if got := resolveVersion(buildStamp{version: "   "}, bi, time.Now()); !snapshotRe.MatchString(got) {
		t.Fatalf("blank tag should fall back to snapshot shape, got %q", got)
	}
}

func TestResolveVersionSnapshotUsesBuildTimeAndHash(t *testing.T) {
	// 无 tag：构建时间（mtime）+ vcs.revision 短 hash。时间固定按 +08:00 格式化，
	// 与仓库时间戳 tag 的 +0800 墙钟口径一致。
	at := time.Date(2026, 10, 27, 20, 38, 0, 0, versionZone)
	bi := buildInfoOf("vcs.revision", "0123456789abcdef0123456789abcdef01234567")
	got := resolveVersion(buildStamp{}, bi, at)
	if want := "v2026.10.27.2038-0123456"; got != want {
		t.Fatalf("snapshot version = %q, want %q", got, want)
	}
	if !snapshotRe.MatchString(got) {
		t.Fatalf("snapshot version %q does not match contract shape %s", got, snapshotRe)
	}

	// 注入的 commit 优先于 VCS 戳记（CI/容器里靠它把 hash 带进镜像）。
	got = resolveVersion(buildStamp{commit: "ABCDEF0deadbeef"}, bi, at)
	if want := "v2026.10.27.2038-abcdef0"; got != want {
		t.Fatalf("injected commit version = %q, want %q", got, want)
	}
}

func TestResolveVersionSnapshotTimeSources(t *testing.T) {
	at := time.Date(2026, 10, 27, 20, 38, 0, 0, versionZone)
	// RFC3339 注入值优先于 mtime，并换算到本机时区展示。
	rfc := time.Date(2026, 10, 27, 12, 38, 0, 0, time.UTC)
	got := resolveVersion(buildStamp{time: rfc.Format(time.RFC3339), commit: "0123456789"}, nil, at)
	if want := "v" + rfc.In(versionZone).Format("2006.01.02.1504") + "-0123456"; got != want {
		t.Fatalf("rfc3339 build time = %q, want %q", got, want)
	}

	// Unix 秒同样被接受（docker build-arg 里省掉格式转换的写法）。
	unix := time.Unix(1793000000, 0)
	got = resolveVersion(buildStamp{time: "1793000000", commit: "0123456789"}, nil, at)
	if want := "v" + unix.In(versionZone).Format("2006.01.02.1504") + "-0123456"; got != want {
		t.Fatalf("unix build time = %q, want %q", got, want)
	}

	// 注入的时间认不出：回落到 mtime，而不是拿坏值拼一个错误的时间。
	got = resolveVersion(buildStamp{time: "not-a-time", commit: "0123456789"}, nil, at)
	if want := "v2026.10.27.2038-0123456"; got != want {
		t.Fatalf("bogus build time = %q, want mtime fallback %q", got, want)
	}

	// mtime 拿不到（某些只读/被搬走的二进制）：最后才回落 vcs.time。
	bi := buildInfoOf("vcs.revision", "0123456789", "vcs.time", rfc.Format(time.RFC3339))
	got = resolveVersion(buildStamp{}, bi, time.Time{})
	if want := "v" + rfc.In(versionZone).Format("2006.01.02.1504") + "-0123456"; got != want {
		t.Fatalf("vcs.time fallback = %q, want %q", got, want)
	}
}

func TestResolveVersionNoHashNoVCS(t *testing.T) {
	at := time.Date(2026, 10, 27, 20, 38, 0, 0, versionZone)
	// 有构建时间但没有提交号：只给时间，不留一个悬空的 "-"。
	got := resolveVersion(buildStamp{}, buildInfoOf("vcs.revision", "unknown"), at)
	if want := "v2026.10.27.2038"; got != want {
		t.Fatalf("version without commit = %q, want %q", got, want)
	}

	// 什么都没有：如实说不知道（docker build 未传 build-arg 的典型形态）。
	if got := resolveVersion(buildStamp{}, nil, time.Time{}); got != "dev" {
		t.Fatalf("version without any source = %q, want dev", got)
	}
}

func TestResolveVersionDirtyStaysOutOfVersion(t *testing.T) {
	// 脏工作区只在启动日志里标注：版本串要能塞进 208px 侧栏，末尾的 hash 不能被截掉。
	at := time.Date(2026, 10, 27, 20, 38, 0, 0, versionZone)
	bi := buildInfoOf("vcs.revision", "0123456789abcdef", "vcs.modified", "true")
	got := resolveVersion(buildStamp{}, bi, at)
	if want := "v2026.10.27.2038-0123456"; got != want {
		t.Fatalf("dirty build version = %q, want %q (dirty must not leak into version)", got, want)
	}
	if !snapshotRe.MatchString(got) {
		t.Fatalf("dirty build version %q broke the contract shape", got)
	}
}

func TestParseBuildTime(t *testing.T) {
	if got := parseBuildTime("  "); !got.IsZero() {
		t.Fatalf("blank parseBuildTime = %v, want zero", got)
	}
	if got := parseBuildTime("2026-13-40"); !got.IsZero() {
		t.Fatalf("invalid parseBuildTime = %v, want zero", got)
	}
	if got := parseBuildTime("1793000000"); got.Unix() != 1793000000 {
		t.Fatalf("unix parseBuildTime = %v, want 1793000000", got)
	}
}
