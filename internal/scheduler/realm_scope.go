// realm_scope.go 任务作用的「域作用域」：让批量任务（签到 / 活跃 / 旅行 / 保活 /
// 余额 / 连登 / 夜猫子）能只对某个域（cn 或 global）的账号执行。
//
// 为什么需要：面板把国内版与国际版账号分开管理之后，运维要能「只给国内版签到」
// 「只刷国际版余额」——此前所有 Run*Now 都是全池语义，混池时想按域操作只能
// 一个个点单号按钮。域过滤是**附加谓词**，空串退化为现状（全池），既有调用点
// 与既有行为零变化。
package scheduler

import (
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// realmCN / realmGlobal 域枚举（与 auth.Realm() 归一化输出一致）。
const (
	realmCN     = "cn"
	realmGlobal = "global"
)

// normalizeRealm 归一化外部传入的域作用域：空串 / 未知值 → ""（= 全池，现状语义）。
//
// 这里刻意不因为"传了个怪值"就报错或返回空集合：域作用域是运维便利开关，
// 拼错一个字母导致「一个账号都没跑到」比「跑全池」更糟（静默失效）。
// 未知值一律回落全池，与 auth.ResolveRealm 的保守方向一致。
func normalizeRealm(realm string) string {
	switch realm {
	case realmCN:
		return realmCN
	case realmGlobal:
		return realmGlobal
	default:
		return ""
	}
}

// inScope 报告账号是否落在域作用域内：realm=="" 恒 true（全池）。
// 判定走 auth.Realm()（与选号/路由/计数完全同源，含 global.enabled 逃生门语义）。
func inScope(a *auth.Auth, realm string) bool {
	if realm == "" {
		return true
	}
	if a == nil {
		return false
	}
	return a.Realm() == realm
}
