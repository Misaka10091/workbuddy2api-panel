// apikeys.go 面板「API 密钥」管理：多密钥增删改查，每个密钥可绑定国内版 / 国际版。
//
// 语义边界：
//   - config.api_key 是**主密钥**（不限域，面板鉴权也用它），本页不管理它、也不允许
//     删除它——删了会把自己锁在门外。页面上以只读行展示其存在。
//   - 密钥表里的每把密钥面向**下游客户端**，可绑定单一域（cn/global）或留空不限。
//     绑定后该密钥的请求只会落到对应域的账号上（server 侧 withAuth + chatCompletions）。
//   - 写操作即刻热生效（server 与 panel 共享同一个 apikeys.Store），并通过 SaveKeys
//     落盘 config.json，重启后仍在。
package panel

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/apikeys"
)

// apiKeysList 返回密钥列表。
//
// 默认**脱敏**回显：密钥是长期凭证，列表页在共享屏幕/截图场景太容易泄漏。
// ?reveal=1 显式要求明文（用于「我刚建的，复制一下」这种一次性场景），且只回明文
// 不落日志。主密钥（config.api_key）单独一项，恒脱敏、标注 primary 不可删。
func (p *Panel) apiKeysList(w http.ResponseWriter, r *http.Request) {
	if p.cfg.APIKeys == nil {
		writeErr(w, http.StatusNotImplemented, "multi-key management not available")
		return
	}
	reveal := r.URL.Query().Get("reveal") == "1"
	list := p.cfg.APIKeys.List()
	rows := make([]map[string]any, 0, len(list)+1)

	// 主密钥行：只为让运维看到「还有一把不受域限制的钥匙存在」，不提供编辑。
	if primary := p.apiKey(); primary != "" {
		rows = append(rows, map[string]any{
			"name":      "主密钥（config.api_key）",
			"key":       apikeys.Redact(primary),
			"realm":     "", // 不限域
			"realm_label": apikeys.RealmLabel(""),
			"primary":   true,
			"disabled":  false,
			"note":      "不限域；面板鉴权与跨域运维用。不可在此删除。",
		})
	}

	for _, k := range list {
		shown := apikeys.Redact(k.Key)
		if reveal {
			shown = k.Key
		}
		rows = append(rows, map[string]any{
			"name":         k.Name,
			"key":          shown,
			"realm":        k.Realm,
			"realm_label":  apikeys.RealmLabel(k.Realm),
			"primary":      false,
			"disabled":     k.Disabled,
			"note":         k.Note,
			"created_at":   k.CreatedAt,
			"allow_models": k.AllowModels,
			"deny_models":  k.DenyModels,
			// model_filter 是给前端直接展示的一句话摘要（避免前端再实现一遍
			// 白/黑名单的语义拼装）；空串 = 不限制。
			"model_filter": describeFilter(k.AllowModels, k.DenyModels),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"keys":   rows,
		"reveal": reveal,
		// realms 供前端渲染域选择器（含中文标签，避免前端各写一份映射）。
		"realms": []map[string]string{
			{"value": apikeys.RealmCN, "label": apikeys.RealmLabel(apikeys.RealmCN)},
			{"value": apikeys.RealmGlobal, "label": apikeys.RealmLabel(apikeys.RealmGlobal)},
			{"value": apikeys.RealmAny, "label": apikeys.RealmLabel(apikeys.RealmAny)},
		},
	})
}

// describeFilter 把白/黑名单拼成一句人话（面板列表与确认提示共用）。
// 空规则返回空串（前端据此显示「不限制」）。
func describeFilter(allow, deny []string) string {
	parts := make([]string, 0, 2)
	if len(allow) > 0 {
		parts = append(parts, "仅允许 "+strings.Join(allow, ", "))
	}
	if len(deny) > 0 {
		parts = append(parts, "排除 "+strings.Join(deny, ", "))
	}
	return strings.Join(parts, "；")
}

// apiKeyCreate 新建一把密钥：{name, realm, note, key?, allow_models?, deny_models?}。
// key 留空则服务端生成随机值并**在响应里返回明文一次**（此后只能靠 reveal 再看）。
//
// 模型过滤随密钥一起落定：白名单为空 = 不限制；黑名单优先于白名单。
// 两条列表都支持通配符（* / ?），语义见 apikeys.ModelFilter。
func (p *Panel) apiKeyCreate(w http.ResponseWriter, r *http.Request) {
	if !p.keysWritable(w) {
		return
	}
	var body struct {
		Name        string   `json:"name"`
		Realm       string   `json:"realm"`
		Note        string   `json:"note"`
		Key         string   `json:"key"`
		AllowModels []string `json:"allow_models"`
		DenyModels  []string `json:"deny_models"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	key := strings.TrimSpace(body.Key)
	if key == "" {
		generated, err := apikeys.Generate()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		key = generated
	}
	created, err := p.cfg.APIKeys.Add(apikeys.Key{
		Name:        body.Name,
		Key:         key,
		Realm:       body.Realm,
		Note:        body.Note,
		AllowModels: body.AllowModels,
		DenyModels:  body.DenyModels,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := p.persistKeys(); err != nil {
		// 落盘失败不回滚内存：本次进程内已生效，重启会丢——把错误如实告诉运维，
		// 而不是假装成功（回滚会让「刚建的密钥突然 401」更难理解）。
		log.Printf("panel: api key 落盘失败（内存已生效，重启会丢）: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "key": created.Key, "name": created.Name, "realm": created.Realm,
			"model_filter": describeFilter(created.AllowModels, created.DenyModels),
			"persist_error": err.Error(),
		})
		return
	}
	log.Printf("panel: 新建 API 密钥 name=%q realm=%s filter=[%s]（%s）",
		created.Name, created.Realm, describeFilter(created.AllowModels, created.DenyModels), apikeys.Redact(created.Key))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"key":   created.Key, // 明文只在这里返回一次
		"name":  created.Name,
		"realm": created.Realm,
		"allow_models": created.AllowModels,
		"deny_models":  created.DenyModels,
		"model_filter": describeFilter(created.AllowModels, created.DenyModels),
	})
}

// apiKeyUpdate 修改一把密钥的可变字段：
// {key(定位), name?, realm?, disabled?, note?, allow_models?, deny_models?}。
// key 本身不可改（对外契约）——换值请用 rotate。
//
// 模型过滤字段的「不改」与「清空」要区分开：JSON 里字段缺失 = 不改；
// 显式给 [] = 清空。前端把规则全删光时必须能表达「恢复不限制」。
func (p *Panel) apiKeyUpdate(w http.ResponseWriter, r *http.Request) {
	if !p.keysWritable(w) {
		return
	}
	var body struct {
		Key         string    `json:"key"`
		Name        *string   `json:"name"`
		Realm       *string   `json:"realm"`
		Disabled    *bool     `json:"disabled"`
		Note        *string   `json:"note"`
		AllowModels *[]string `json:"allow_models"`
		DenyModels  *[]string `json:"deny_models"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// 面板列表里回显的是脱敏值，前端提交的 key 可能是脱敏形——此时按「仅一个匹配」
	// 或 name 定位会歧义，故要求前端始终带完整明文/或由前端保存明文。这里做兜底：
	// 若定位失败且只传了 name，就按 name 唯一定位（便于手改）。
	target := body.Key
	if _, found := p.cfg.APIKeys.Lookup(target); !found {
		if body.Name != nil {
			if match, ok := p.findKeyByName(*body.Name); ok {
				target = match
			}
		}
	}
	updated, err := p.cfg.APIKeys.Update(target, apikeys.KeyUpdate{
		Name:        body.Name,
		Realm:       body.Realm,
		Disabled:    body.Disabled,
		Note:        body.Note,
		AllowModels: body.AllowModels,
		DenyModels:  body.DenyModels,
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := p.persistKeys(); err != nil {
		log.Printf("panel: api key 落盘失败: %v", err)
	}
	log.Printf("panel: 更新 API 密钥 name=%q realm=%s disabled=%v filter=[%s]",
		updated.Name, updated.Realm, updated.Disabled, describeFilter(updated.AllowModels, updated.DenyModels))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": updated.Name, "realm": updated.Realm, "disabled": updated.Disabled,
		"allow_models": updated.AllowModels,
		"deny_models":  updated.DenyModels,
		"model_filter": describeFilter(updated.AllowModels, updated.DenyModels),
	})
}

// apiKeyRemove 删除一把密钥：{key}。
func (p *Panel) apiKeyRemove(w http.ResponseWriter, r *http.Request) {
	if !p.keysWritable(w) {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := p.cfg.APIKeys.Remove(body.Key); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := p.persistKeys(); err != nil {
		log.Printf("panel: api key 落盘失败: %v", err)
	}
	log.Printf("panel: 删除 API 密钥 %s", apikeys.Redact(body.Key))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiKeyRotate 轮换密钥值：{key} → 返回新明文，旧值立即失效。
func (p *Panel) apiKeyRotate(w http.ResponseWriter, r *http.Request) {
	if !p.keysWritable(w) {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	rotated, err := p.cfg.APIKeys.Rotate(body.Key)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := p.persistKeys(); err != nil {
		log.Printf("panel: api key 轮换后落盘失败: %v", err)
	}
	log.Printf("panel: 轮换 API 密钥 name=%q → %s（旧值已失效）", rotated.Name, apikeys.Redact(rotated.Key))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "key": rotated.Key, "name": rotated.Name, "realm": rotated.Realm,
	})
}

// ---------------------------------------------------------------- helpers

// keysWritable 写操作前置检查：密钥表存在 + 有落盘闭包。
func (p *Panel) keysWritable(w http.ResponseWriter) bool {
	if p.cfg.APIKeys == nil || p.cfg.SaveKeys == nil {
		writeErr(w, http.StatusNotImplemented, "multi-key management not available")
		return false
	}
	return true
}

// persistKeys 把当前密钥表写回 config.json。
func (p *Panel) persistKeys() error {
	if p.cfg.SaveKeys == nil {
		return nil
	}
	return p.cfg.SaveKeys(p.cfg.APIKeys.List())
}

// findKeyByName 按 name 唯一定位密钥值；重名/未找到返回 false。
// 只用于「前端提交了脱敏 key」的兜底，避免误删/误改同名条目。
func (p *Panel) findKeyByName(name string) (string, bool) {
	var (
		hit   string
		count int
	)
	for _, k := range p.cfg.APIKeys.List() {
		if k.Name == name {
			hit = k.Key
			count++
		}
	}
	if count != 1 {
		return "", false
	}
	return hit, true
}

// decodeBody 解析 JSON body（限长 64KiB，密钥管理请求体很小）。
func decodeBody(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}
