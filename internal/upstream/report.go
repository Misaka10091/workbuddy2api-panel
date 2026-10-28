// report.go growth 域「对话活跃上报」接口：POST {chatBase}/v2/report。
// 照抄客户端 chat_request_send 事件形状（含 conversationId/mode/inputLength 等全字段 +
// 桌面指纹，勿用最小 3 字段，防上游后续加严）。事件必须带 userId（=账号 uid，由
// desktopFingerprint 提供），缺失则服务端 200 但静默丢弃（实测见 REPORT-active-map.md §2）。
//
// 一条上报同时点亮 growth 连登 + 解锁 first_buddy 任务（领养前置）。
// 风控口径：每号每天 1 次即可（activity_hours 单时点），不做多时点高频上报。
//
// 2026-10-28（上游 ee1c80d / 5.9.3）：通道从 billingBase 改到 **chatBase**，且不再走
// billingJSON / BillingHeaders —— 真机 /v2/report 是桌面指纹域的 9 键头族（与 billing
// 族的 axios 形态完全不同），故自建 reportHeaders。字段集同步增删见 chatRequestEvent。
package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
)

// reportPath 活跃上报通道（实测）。
const reportPath = "/v2/report"

// reportHeaders /v2/report 的专用头族（5.9.3 真机 9 键黄金集，剔除 host /
// connection / content-length / accept-encoding / x-device-token / content-encoding
// 六类传输头后）：accept / authorization / content-type / user-agent / x-domain /
// x-product / x-request-id / x-requested-with / x-user-id。
//
// 为什么单独一份而不是复用 CommonHeaders：桌面用户代理是两段同品牌（desktopUAFor），
// Accept 是宽松值、Content-Type 带 charset、X-Domain 取 chat 域裸主机名，且**不带**
// X-Device-Token（只有 billing 族带）。ReportDesktopEvent 与 chat 活跃上报共用本函数，
// 两处一旦各写一份就会再次漂移。
func (c *Client) reportHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Request-ID", session.NewMessageID())
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", c.desktopUAFor(a))
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	req.Header.Set("X-Domain", c.chatHost(a))
}

// billingJSON 发 billing 域（billingBase）请求并解信封。body 为 nil 时按方法补默认体：
// POST 补 `{}`（真机 billing 族 POST 的 Content-Length 恒 2），其余方法不带体。
// 与 travel.go 的 growthJSON 对称（growth 域走 chatBase + BillingHeaders；billing 域走 billingBase）。
// 信封与错误语义同 doJSON。
//
// X-Request-ID 只在这里注入，**不进 BillingHeaders** —— BillingHeaders 被 travel.go /
// tasks.go 复用于 chatBase 的 /activity/growth/*，那几条路径没有抓包证据，共用头族里
// 塞新头会外溢成新的形态偏差。
func (c *Client) billingJSON(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	} else if method == http.MethodPost {
		// 真机 billing 族 POST（checkin-activity-status 等）Content-Length 恒 2、body 是 {}。
		rdr = strings.NewReader("{}")
	}
	req, err := http.NewRequest(method, c.billingBase(a)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.BillingHeaders(req, a)
	req.Header.Set("X-Request-ID", session.NewMessageID())
	return c.doJSON(req)
}

// billingMeterJSON 仅对 /billing/meter 族端点（get-user-resource / daily-checkin）
// 按 realm 走双路径 fallback：global 先无 /v2 前缀，ErrNotFound 时二次换有 /v2 前缀
// （上游新旧路径分叉）；cn 单路径（有 /v2）现状不变。仅 global realm 才有多路径。
func (c *Client) billingMeterJSON(a *auth.Auth, paths []string, method string, body any) (json.RawMessage, error) {
	var lastErr error
	for i, path := range paths {
		data, err := c.billingJSON(a, method, path, body)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// 仅 404 换路径（路径不存在才值得 fallback）；其他错误直接返回。
		var ue *Error
		if !errors.As(err, &ue) || ue.Kind != ErrNotFound || i == len(paths)-1 {
			return nil, err
		}
	}
	return nil, lastErr
}

// billingRetryDelay 签到/余额等维护类计费调用瞬时错误重试的间隔基数。
// 独立变量供测试缩短（生产固定 2s：第 1 次重试等 2s、第 2 次等 4s）。
var billingRetryDelay = 2 * time.Second

// isTransientBillingErr 报告 err 是否值得对计费维护类调用做有界重试：
// 上游 5xx（ErrServer，实测偶发 "code 10000 / API request failed with status
// code: 500"）或网络层错误（非 *Error 的传输失败）。业务错误（code!=0 的
// 已签到/参数错、4xx、限流）不重试——重试只会原样再失败一次。
func isTransientBillingErr(err error) bool {
	if err == nil {
		return false
	}
	var ue *Error
	if errors.As(err, &ue) {
		return ue.Kind == ErrServer
	}
	return true
}

// retryBillingTransient 对签到/余额这类低频维护调用做瞬时错误有界重试：
// 最多补打 2 次（间隔 2s、4s），首次成功或非瞬时错误立即返回。chat 热路径
// 不用本策略——它有自己的换号轮转语义，重试会放大在途请求。
func (c *Client) retryBillingTransient(fn func() error) error {
	err := fn()
	if err == nil || !isTransientBillingErr(err) {
		return err
	}
	for i := 1; i <= 2; i++ {
		time.Sleep(time.Duration(i) * billingRetryDelay)
		if err = fn(); err == nil || !isTransientBillingErr(err) {
			return err
		}
	}
	return err
}

// chatRequestEvent 客户端 chat_request_send 事件完整形状（5.9.3 真机 report 事件）。
// userId / timestamp / reportDelay / presentAt 由 desktopFingerprint 提供，本结构不再
// 重复持有（同一字段两处写值必然漂移）。
//
// 与 5.8.x 相比删掉的字段（上游 ee1c80d）：maxToken / maxSteps / temperature /
// maxRetries / skillCount / totalCount / fileUri / expertId —— 真机这条事件里不再出现，
// 多发一个客户端不发的键同样是指纹。
type chatRequestEvent struct {
	EventCode             string `json:"eventCode"`
	Mode                  string `json:"mode"`
	ConversationID        string `json:"conversationId"`
	RequestID             string `json:"requestId"`
	InputLength           int    `json:"inputLength"`
	RequestModelID        string `json:"requestModelId"`
	RequestModelName      string `json:"requestModelName"`
	IsPlan                bool   `json:"isPlan"`
	IsAutoExecuteTerminal bool   `json:"isAutoExecuteTerminal"`
	IsAutoModify          bool   `json:"isAutoModify"`
	CodebaseEnable        bool   `json:"codebaseEnable"`
	MentionContexts       []any  `json:"mentionContexts"`
	KnowledgeID           []any  `json:"knowledgeId"`
	KnowledgeName         []any  `json:"knowledgeName"`
	CodebaseID            string `json:"codebaseId"`
	MentionContextCount   int    `json:"mentionContextCount"`
	Command               string `json:"command"`
	RecommendID           string `json:"recommendId"`
	SkillID               string `json:"skillId"`
	TraceID               string `json:"traceId"`
	RootRequestID         string `json:"rootRequestId"`
	ParentConversationID  string `json:"parentConversationId"`
	AgentName             string `json:"agentName"`
	AgentType             string `json:"agentType"`

	// 5.9.3 新增维度：Max 模式标记、来源、输入尝试 ID 与 VCS 段（真机恒 unknown + 三空串）。
	IsMaxMode      bool   `json:"isMaxMode"`
	Source         string `json:"source"`
	InputAttemptID string `json:"inputAttemptId"`
	VcsType        string `json:"vcsType"`
	VcsRepo        string `json:"vcsRepo"`
	VcsBranchName  string `json:"vcsBranchName"`
	VcsRevID       string `json:"vcsRevId"`

	// buddyId / buddyName 恒空但保留键（真机序列化保留空值对）。
	BuddyID   string `json:"buddyId"`
	BuddyName string `json:"buddyName"`

	// OTel 风格的点号键：与同请求的会话 ID / 轮级 ID 同值。
	CodebuddySessionID             string `json:"codebuddy.session_id"`
	CodebuddyConversationRequestID string `json:"codebuddy.conversation_request_id"`
}

// reportDefaultModelID / reportDefaultModelName ReportChatActivity 的缺省上报模型
// （5.9.3 真机默认模型从 deepseek-v4-flash 换成 deepseek-v4.1-flash）。
const (
	reportDefaultModelID   = "deepseek-v4.1-flash"
	reportDefaultModelName = "DeepSeek-V4.1-Flash"
)

// ReportChatActivity 向上游发送一条对话活跃上报（chat_request_send）。
// conversationID 由调用方生成（如 wb2api-<ms>），无需真实会话——服务端不校验一致性。
// requestID 为本轮请求独立标识（多轮同会话上报时各条不同）；空时回落 conversationID。
// 错误语义与 doJSON 一致：HTTP 非 2xx / 业务 code != 0 → *Error。
func (c *Client) ReportChatActivity(a *auth.Auth, conversationID, requestID string) error {
	return c.ReportChatActivityModel(a, conversationID, requestID, reportDefaultModelID, reportDefaultModelName)
}

// ReportChatActivityModel 同上，但可指定上报携带的模型：供「体验某模型」类任务
// 对齐实际模型（如 Model_chat_GLM5.2 需 requestModelId=glm-5.2 与独立 requestID）。
func (c *Client) ReportChatActivityModel(a *auth.Auth, conversationID, requestID, modelID, modelName string) error {
	if requestID == "" {
		requestID = conversationID
	}
	if modelID == "" {
		modelID = reportDefaultModelID
	}
	if modelName == "" {
		modelName = modelID
	}
	ev := chatRequestEvent{
		EventCode:             "chat_request_send",
		Mode:                  "craft",
		ConversationID:        conversationID,
		RequestID:             requestID,
		InputLength:           12,
		RequestModelID:        modelID,
		RequestModelName:      modelName,
		IsPlan:                false,
		IsAutoExecuteTerminal: false,
		IsAutoModify:          false,
		CodebaseEnable:        false,
		MentionContexts:       []any{},
		KnowledgeID:           []any{},
		KnowledgeName:         []any{},
		CodebaseID:            "",
		MentionContextCount:   0,
		Command:               "",
		RecommendID:           "",
		SkillID:               "",
		TraceID:               session.NewTraceID(),
		RootRequestID:         requestID,
		ParentConversationID:  conversationID,
		AgentName:             "cli",
		AgentType:             AgentTypeMain,
		IsMaxMode:             true,
		Source:                "work",
		InputAttemptID:        session.NewConversationID(),
		VcsType:               "unknown",
		VcsRepo:               "",
		VcsBranchName:         "",
		VcsRevID:              "",
		BuddyID:               "",
		BuddyName:             "",

		CodebuddySessionID:             conversationID,
		CodebuddyConversationRequestID: requestID,
	}
	// 桌面指纹打底 + 业务字段优先（与 ReportDesktopEvent 同口径）：userId / timestamp /
	// reportDelay / presentAt / 设备三元组都由 fingerprint 提供，事件结构不重复持有。
	m := map[string]any{}
	for k, v := range desktopFingerprint(a) {
		m[k] = v
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var biz map[string]any
	if err := json.Unmarshal(raw, &biz); err != nil {
		return err
	}
	for k, v := range biz {
		m[k] = v
	}
	payload, err := json.Marshal([]map[string]any{m})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.chatBase(a)+reportPath, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.reportHeaders(req, a)
	_, err = c.doJSON(req)
	return err
}
