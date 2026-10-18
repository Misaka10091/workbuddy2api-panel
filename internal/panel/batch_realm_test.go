package panel

import (
	"net/http/httptest"
	"testing"
)

// TestBatchRealmParam 批量接口的域作用域解析：只认 cn/global，其余一律全池。
//
// 「全池」用空串表示（= 不带 realm 谓词），与历史调用逐字一致：
// 老前端/外部 curl 不带参数时行为不变，这是本次改动零回归的依据。
func TestBatchRealmParam(t *testing.T) {
	cases := []struct{ query, want string }{
		{"", ""},
		{"?realm=", ""},
		{"?realm=cn", "cn"},
		{"?realm=global", "global"},
		{"?realm=CN", ""},         // 大小写不匹配 → 回落全池
		{"?realm=all", ""},        // 前端「全部」不上送，防外部脚本乱传
		{"?realm=%20cn%20", "cn"}, // trim 后合法（前端 encodeURIComponent 不会产生，但人工 curl 会）
		{"?realm=global&x=1", "global"},
		{"?other=cn", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/panel/api/checkin_all"+c.query, nil)
		if got := batchRealm(r); got != c.want {
			t.Errorf("batchRealm(%q)=%q want %q", c.query, got, c.want)
		}
	}
}

// TestBatchRealmLabel 日志标签：把域翻译成中文，全池用「全池」而不是空串
// （日志里出现「手动签到已触发（）」会让人以为域参数丢了）。
func TestBatchRealmLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "全池"},
		{"cn", "国内版"},
		{"global", "国际版"},
		{"foo", "全池"}, // 未知值按全池记（与 batchRealm 的回落一致）
	}
	for _, c := range cases {
		if got := batchRealmLabel(c.in); got != c.want {
			t.Errorf("batchRealmLabel(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
