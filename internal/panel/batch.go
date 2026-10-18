// batch.go 面板账号表的批量操作：对选中的多个账号执行同一动作。
//
// 设计取舍：
//   - 单一端点 POST /panel/api/accounts/batch，body {action, uids[]}，而不是给每个
//     动作各开一个 /batch 路由。六种动作语义同构（遍历 uid 做一件事 + 收集结果），
//     开六个路由只是把同一段分派代码抄六遍。
//   - 逐账号独立执行、**部分失败不回滚**：批量禁用 5 个号时第 3 个不存在，
//     另外 4 个该禁的还是要禁。回滚会让"结果取决于顺序"的诡异状态更难理解。
//     每个账号的结果单独返回，前端据此显示"N 成功 / M 失败（原因）"。
//   - 上限保护：一次最多处理 maxBatchAccounts 个，防止误传全量 uid 打爆上游
//     （保活/签到/余额这类会真的发网络请求）。
package panel

import (
	"fmt"
	"log"
	"net/http"
	"strings"
)

// maxBatchAccounts 单次批量操作的账号数上限。
//
// 为什么需要：checkin/balance/keepalive 每个账号都会打一次上游；误操作（比如前端
// 全选后点错）把几百个号一起刷，既可能触发风控，也会让面板请求超时。
// 50 对个人/小团队部署足够，同时留出明显的"操作规模过大"信号。
const maxBatchAccounts = 50

// batchAction 支持的批量动作（与单号接口一一对应）。
var batchActions = map[string]string{
	"checkin":   "签到",
	"balance":   "刷新余额",
	"revive":    "解冻",
	"disable":   "禁用",
	"pause":     "暂停选号",
	"resume":    "恢复选号",
	"keepalive": "保活",
	"remove":    "移除",
}

// batchResult 单个账号的执行结果。
type batchResult struct {
	UID   string `json:"uid"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Note 附加信息（如签到的业务提示"今天已签到"、移除的文件删除失败）。
	Note string `json:"note,omitempty"`
}

// accountsBatch 批量执行一个动作：body {"action":"disable","uids":["a","b"]}。
//
// 返回 {"ok":true,"action":"disable","total":N,"succeeded":N,"failed":N,"results":[...]}。
// 整体恒 200：单账号失败是"业务结果"而不是"请求失败"，用 results 表达；
// 只有请求本身不合法（未知 action、空 uids、超上限）才 4xx。
func (p *Panel) accountsBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string   `json:"action"`
		UIDs   []string `json:"uids"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	action := strings.TrimSpace(body.Action)
	label, known := batchActions[action]
	if !known {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("unknown action %q (supported: %s)", action, strings.Join(batchActionNames(), ", ")))
		return
	}

	// 去重 + 去空白：前端「全选」可能因重渲染带上重复 uid，重复执行签到会
	// 白白多打一次上游（虽然幂等，但没必要）。
	uids := dedupeUIDs(body.UIDs)
	if len(uids) == 0 {
		writeErr(w, http.StatusBadRequest, "uids must not be empty")
		return
	}
	if len(uids) > maxBatchAccounts {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("too many accounts: %d (max %d per batch)", len(uids), maxBatchAccounts))
		return
	}

	// 逐个执行。刻意**不并发**：这些操作多为上游 RPC（签到/余额/保活），
	// 并发会放大风控风险；顺序执行还能保证日志顺序与用户看到的列表一致。
	results := make([]batchResult, 0, len(uids))
	succeeded := 0
	for _, uid := range uids {
		res := p.runBatchAction(action, uid)
		if res.OK {
			succeeded++
		}
		results = append(results, res)
	}
	log.Printf("panel: 批量%s 完成：%d/%d 成功（目标 %d 个账号）",
		label, succeeded, len(uids), len(uids))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"action":    action,
		"label":     label,
		"total":     len(uids),
		"succeeded": succeeded,
		"failed":    len(uids) - succeeded,
		"results":   results,
	})
}

// runBatchAction 在单个账号上执行动作。错误转成 batchResult 而不是中断整批。
func (p *Panel) runBatchAction(action, uid string) batchResult {
	res := batchResult{UID: uid}
	switch action {
	case "revive":
		if _, ok := p.cfg.Pool.Status(uid); !ok {
			res.Error = "account not found"
			return res
		}
		p.cfg.Pool.Revive(uid)
	case "disable":
		if _, ok := p.cfg.Pool.Status(uid); !ok {
			res.Error = "account not found"
			return res
		}
		p.cfg.Pool.Disable(uid, "manual disable (panel batch)")
	case "pause":
		if !p.cfg.Pool.Pause(uid) {
			res.Error = "account not found"
			return res
		}
	case "resume":
		if !p.cfg.Pool.Resume(uid) {
			res.Error = "account not found"
			return res
		}
	case "remove":
		a := p.cfg.Pool.Remove(uid)
		if a == nil {
			res.Error = "account not found"
			return res
		}
		// 文件删除失败不视为失败：账号已出池（这才是用户要的效果），
		// 残留文件作为 Note 暴露，便于手工清理。
		if a.FilePath != "" {
			if err := removeFile(a.FilePath); err != nil {
				res.Note = "auth 文件删除失败：" + err.Error()
			}
		}
	case "checkin":
		note, err := p.checkinOne(uid)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Note = note
	case "balance":
		note, err := p.balanceOne(uid)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Note = note
	case "keepalive":
		note, err := p.keepaliveOne(uid)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Note = note
	default:
		res.Error = "unsupported action: " + action
		return res
	}
	res.OK = true
	return res
}

// batchActionNames 返回按固定顺序排列的动作名（错误提示与测试用，保证稳定输出）。
func batchActionNames() []string {
	return []string{"checkin", "balance", "keepalive", "revive", "disable", "pause", "resume", "remove"}
}

// dedupeUIDs 去空白、去重、保持原顺序。
func dedupeUIDs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}
