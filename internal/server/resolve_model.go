package server

import "strings"

// resolveModel 解析模型名协议（PLAN D6）：
//
//	分布式前缀： "[realm:]model"
//
// 取第一个 ":"，前段恰为 "cn"/"global" 才剥离；否则视为裸名，realm=cn、bare=原串。
// 大小写敏感（前缀必须是精确的小写枚举）。bare 即出站/选号/账本使用的裸模型名。
//
// 导出为 ResolveModel（cmd/server/main.go 粘性闭包需要），包内简写 resolveModel。
func resolveModel(model string) (realm, bare string) {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return "cn", model
	}
	prefix := model[:idx]
	if prefix != "cn" && prefix != "global" {
		return "cn", model
	}
	return prefix, model[idx+1:]
}

// ResolveModel 是 resolveModel 的导出面（跨包调用）。
func ResolveModel(model string) (realm, bare string) { return resolveModel(model) }

// explicitRealmPrefix 报告模型名是否**显式**带了 "cn:" / "global:" 域前缀。
//
// 与 resolveModel 的区别：后者无法区分「裸名」（裸名也回落 cn）与「显式 cn: 前缀」，
// 而多密钥场景需要这个区别——裸名要按密钥绑定的域走，只有显式前缀才需要判
// 「是否与密钥域冲突」。大小写敏感（与 resolveModel 的枚举一致）。
func explicitRealmPrefix(model string) bool {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return false
	}
	switch model[:idx] {
	case "cn", "global":
		return true
	default:
		return false
	}
}
