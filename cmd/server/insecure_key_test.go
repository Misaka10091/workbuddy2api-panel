// insecure_key_test.go 弱密钥 / 非回环监听的启动告警矩阵。
package main

import "testing"

func TestInsecureKeyWarning(t *testing.T) {
	cases := []struct {
		name, listen, key string
		want              bool
	}{
		{"回环+空key", "127.0.0.1:7863", "", false},
		{"回环+test_key", "localhost:7863", "test_key", false},
		{"回环+v6", "[::1]:7863", "test_key", false},
		{"全网卡+空key", ":7863", "", true},
		{"全网卡+test_key", "0.0.0.0:7863", "test_key", true},
		{"全网卡+强key", ":7863", "s3cret-strong", false},
		{"具名地址+空key", "10.0.0.5:7863", "", true},
	}
	for _, c := range cases {
		got := insecureKeyWarning(c.listen, c.key) != ""
		if got != c.want {
			t.Errorf("%s: warn=%v want %v", c.name, got, c.want)
		}
	}
}
