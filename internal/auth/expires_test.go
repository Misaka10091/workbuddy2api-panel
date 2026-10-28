package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNormalizeExpiresAt 过期时间归一：毫秒时间戳必须折成秒，合法秒值幂等。
//
// 守护语义：毫秒值被当秒用会被解释成极远未来 → NeedsRefresh 永假 → token 永不刷新
// → 真过期后账号直接不可用，且全程没有报错（只能靠"突然全 401"倒推）。
func TestNormalizeExpiresAt(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"秒值原样", 1761600000, 1761600000},
		{"毫秒归一", 1761600000000, 1761600000},
		{"零值", 0, 0},
		{"负值按无到期", -5, 0},
		{"阈值等价边界（秒）", expiresAtMillisThreshold, expiresAtMillisThreshold},
		{"阈值+1 判毫秒", expiresAtMillisThreshold + 1, (expiresAtMillisThreshold + 1) / 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeExpiresAt(tc.in); got != tc.want {
				t.Fatalf("normalizeExpiresAt(%d)=%d want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseMillisExpiresAtNeedsRefresh 毫秒形态的 expiresAt 导入后必须判为「需要刷新」，
// 而不是被当成几十年后的到期时间。
func TestParseMillisExpiresAtNeedsRefresh(t *testing.T) {
	ms := time.Now().Add(-time.Hour).UnixMilli() // 已经过期一小时（毫秒表示）

	nested := fmt.Sprintf(`{"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":%d},"account":{"uid":"u1"}}`, ms)
	flat := fmt.Sprintf(`{"accessToken":"at","refreshToken":"rt","expiresAt":%d,"uid":"u1"}`, ms)

	for name, raw := range map[string][]byte{"嵌套形": []byte(nested), "扁平形": []byte(flat)} {
		t.Run(name, func(t *testing.T) {
			a, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := a.ExpiresAtValue(); got != ms/1000 {
				t.Fatalf("ExpiresAt=%d want %d（毫秒应折成秒）", got, ms/1000)
			}
			if !a.NeedsRefresh(0) {
				t.Fatal("已过期的毫秒时间戳必须判为需要刷新（否则 token 永不刷新）")
			}
		})
	}
}

// TestSaveAtomicWritesValidFileAndLeavesNoTmp 落盘成功后不留 .tmp，
// 且文件内容是能被 Parse 读回的完整凭证（fsync + rename 之后才删临时文件）。
func TestSaveAtomicWritesValidFileAndLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy.json")
	a := &Auth{
		AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: 1761600000, UID: "u1", FilePath: fp,
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	if _, err := os.Stat(fp + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("落盘成功后不应残留 .tmp: %v", err)
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("落盘内容应可被 Parse 读回: %v", err)
	}
	if back.AccessTokenValue() != "at" || back.RefreshTokenValue() != "rt" || back.ExpiresAtValue() != 1761600000 {
		t.Fatalf("回写内容不一致: %+v", back)
	}
}
