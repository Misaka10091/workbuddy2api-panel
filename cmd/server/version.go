// version.go 二进制自报的版本号：tag 构建取 tag，其余取「构建时间 + 提交短 hash」。
//
// 两档的用意：tag 是人工发布语义（v2026.10.27.2038），日常/本地构建没有 tag，
// 用「构建时间 + 提交短 hash」也能一眼定位到"哪个树、什么时候编的"。面板左上角
// 与底部状态行展示的就是这个串，启动日志再多带提交号（截 12 位）与工作区脏标记。
//
// 取值来源（前者非空即命中）：
//
//	版本  -X main.buildVersion=<tag>（CI 打 tag 时注入）→ 原样展示
//	提交  -X main.buildCommit=<sha>，缺省读 Go 构建信息的 vcs.revision
//	时间  -X main.buildTime=<RFC3339|Unix 秒>，缺省读可执行文件 mtime
//	      （链接完成时刻 ≈ 构建时间；最后才回落到 vcs.time 的提交时间）
//
// 时间按 +08:00 固定格式化（v2006.01.02.1504），与仓库既有时间戳 tag（+0800 墙钟）
// 同一口径；hash 取 7 位。时区钉死是必要的：CI runner 是 UTC、开发机是 +08，不钉死
// 同一份代码在两个环境会差 8 小时，版本串就失去了"什么时候编的"这个意义。
// 用 FixedZone 而不是 LoadLocation：精简容器里没有 tzdata。
//
// 工作区有未提交改动（vcs.modified）只在启动日志里标注，不进版本串：拼上 -dirty
// 之后有 29 个字符，比 208px 侧栏能放的（约 24 个等宽字符）还长，CSS 会截断末尾——
// 而被截掉的正是那个用来定位的 hash。
// 既没有注入也没有 VCS 戳记（docker build 未传 build-arg、-buildvcs=false 等）时
// 退回 "dev"——宁可显示"不知道"，也不要编一个看似精确的版本。
package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// versionZone 版本串里的时间固定按 +08:00 展示（仓库时间戳 tag 的口径）。
var versionZone = time.FixedZone("CST", 8*60*60)

// 由链接期 -X 注入；没有 -X 时保持零值。
var (
	buildVersion string // tag 名（原样展示，不再补 v 前缀）
	buildCommit  string // 提交 sha，可长可短
	buildTime    string // RFC3339 或 Unix 秒
)

// buildInfo 当前二进制的 Go 构建信息（含 VCS 戳记）；读不到时为 nil。
var buildInfo = readBuildInfo()

// appVersion 进程启动即固定的版本串，同时供启动日志与 /panel/api/overview 的
// version 字段使用（面板左上角直接展示，前端不再补 v 前缀）。
var appVersion = resolveVersion(
	buildStamp{version: buildVersion, commit: buildCommit, time: buildTime},
	buildInfo,
	exeModTime(),
)

// buildStamp 链接期注入的三个事实（都可能是空串）。
type buildStamp struct {
	version string
	commit  string
	time    string
}

// resolveVersion 按「注入版本 → 构建时间 + 提交短 hash」的优先级算出展示串。
// 抽成纯函数是为了可测：buildInfo 与 exeMod 都由调用方给，测试不必依赖真实构建环境。
func resolveVersion(st buildStamp, bi *debug.BuildInfo, exeMod time.Time) string {
	if v := strings.TrimSpace(st.version); v != "" {
		return v
	}
	sha := shortSHA(st.commit)
	if sha == "" {
		sha = shortSHA(vcsSetting(bi, "vcs.revision"))
	}
	at := parseBuildTime(st.time)
	if at.IsZero() {
		at = exeMod
	}
	if at.IsZero() {
		at = parseBuildTime(vcsSetting(bi, "vcs.time"))
	}
	if at.IsZero() {
		return "dev"
	}
	v := "v" + at.In(versionZone).Format("2006.01.02.1504")
	if sha != "" {
		v += "-" + sha
	}
	return v
}

// versionLine 启动日志的一行摘要：版本串 + 提交号（截 12 位）+ 工作区脏标记。
// 面板只放得下 21 个字符的版本串，完整定位信息（长提交、脏状态）留在这里。
func versionLine() string {
	sha := strings.TrimSpace(buildCommit)
	if sha == "" {
		sha = vcsSetting(buildInfo, "vcs.revision")
	}
	switch {
	case sha == "":
		sha = "unknown"
	case len(sha) > 12:
		sha = sha[:12]
	}
	line := "wb2api " + appVersion + " (commit " + sha
	if vcsSetting(buildInfo, "vcs.modified") == "true" {
		line += "，工作区有未提交改动"
	}
	return line + ")"
}

// shortSHA 归一化提交号：去空白、转小写、截 7 位；"unknown" 与空串一律视为没有。
func shortSHA(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "unknown" {
		return ""
	}
	if len(s) > 7 {
		s = s[:7]
	}
	return s
}

// parseBuildTime 解析注入的构建时刻：Unix 秒或常见日期时间形态；认不出返回零值
// （调用方据此回落到 mtime / vcs.time，而不是拿一个错误的时间去拼版本串）。
func parseBuildTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// vcsSetting 读 Go 构建信息里的 settings 项（vcs.revision / vcs.time / vcs.modified）。
func vcsSetting(bi *debug.BuildInfo, key string) string {
	if bi == nil {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

func readBuildInfo() *debug.BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return bi
}

// exeModTime 可执行文件的修改时间：go build 落盘即链接完成，是"构建时刻"最接近的
// 近似值（Go 构建信息里没有链接时间戳）。go run 的临时二进制同样适用。
func exeModTime() time.Time {
	p, err := os.Executable()
	if err != nil {
		return time.Time{}
	}
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
