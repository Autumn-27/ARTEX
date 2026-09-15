package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// SettingAutoAllow is the settings 键 for the one-click auto-allow switch
// (「一键放行」). Value is JSON: {"enabled":bool,"expires_at":unix}. A single
// key keeps the state atomic — two keys could disagree on partial writes.
const SettingAutoAllow = "guard_auto_allow"

// AutoAllowState is the resolved auto-allow status returned by the API.
type AutoAllowState struct {
	Enabled          bool  `json:"enabled"`
	ExpiresAt        int64 `json:"expires_at"`
	RemainingSeconds int64 `json:"remaining_seconds"`
}

// autoAllowSetting is the stored form of the setting.
type autoAllowSetting struct {
	Enabled   bool  `json:"enabled"`
	ExpiresAt int64 `json:"expires_at"`
}

// autoAllowHours 夹逼到 {2,4,8}:≤2 → 2,≤4 → 4,其余 → 8。
func autoAllowHours(hours int) int {
	switch {
	case hours <= 2:
		return 2
	case hours <= 4:
		return 4
	default:
		return 8
	}
}

// SetAutoAllow 开启/关闭一键放行。开启时有效期为 hours 小时(夹逼到 2/4/8);
// 关闭时忽略 hours 并清空到期时间。
func (i *Interceptor) SetAutoAllow(enabled bool, hours int) error {
	s := autoAllowSetting{}
	if enabled {
		s.Enabled = true
		s.ExpiresAt = time.Now().Add(time.Duration(autoAllowHours(hours)) * time.Hour).Unix()
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return i.db.SetSetting(SettingAutoAllow, string(raw))
}

// AutoAllowStatus 返回当前一键放行状态。每次调用都做惰性到期检查:已过期则
// 自动关闭(回写 settings 并记日志),等价于到期自动失效,无需后台定时器。
func (i *Interceptor) AutoAllowStatus() AutoAllowState {
	v, ok, err := i.db.GetSetting(SettingAutoAllow)
	if err != nil || !ok {
		return AutoAllowState{}
	}
	var s autoAllowSetting
	if err := json.Unmarshal([]byte(v), &s); err != nil || !s.Enabled {
		return AutoAllowState{}
	}
	remaining := s.ExpiresAt - time.Now().Unix()
	if remaining <= 0 {
		log.Printf("[intercept] 一键放行已到期,自动恢复人工审批")
		if err := i.SetAutoAllow(false, 0); err != nil {
			log.Printf("[intercept] 一键放行到期自动关闭失败: %v", err)
		}
		return AutoAllowState{}
	}
	return AutoAllowState{Enabled: true, ExpiresAt: s.ExpiresAt, RemainingSeconds: remaining}
}

// autoAllowApprove handles an "ask" decision while 一键放行 is active: record
// the approval into history with decision_source = "auto_allow" (审计完整,可
// 区分无人值守放行与人工/模型/规则裁决), then allow without creating a pending
// record. deny 规则不经过这里(guard 的 deny 分支直接拦截,不受本开关影响)。
func (i *Interceptor) autoAllowApprove(ctx context.Context, convID int64, dec Decision, toolName string, input []byte) {
	taskID, agentName := taskInfoFromCtx(ctx)
	audit := auditFor(ctx, dec, input, "allowed")
	reason := fmt.Sprintf("一键放行自动批准: %s", dec.Message)
	id, err := i.db.CreateDecidedInterceptWithSource(dec.RuleID, convID, taskID, agentName, toolName, input, "allowed", reason, "auto_allow", audit)
	if err == nil {
		i.bindResult(ctx, id, audit)
	}
}
