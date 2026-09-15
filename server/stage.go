package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/stage"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// 受管文件投递(F13;stage 包语义见 stage/stage.go 头注释)。本文件做三件事:
//   1. 装配:创建 stage.Manager 并把记账事件写进任务 activity(无任务上下文则只打日志);
//   2. 下载路由 GET /s/{token}/{name}(root mux,JWT 之外——目标机器没有 token;
//      gate 门控对 /s/ 前缀放行,见 gate.go);
//   3. 管理 API(POST/GET/DELETE /api/stage*,JWT 之后)+ worker 的 stage_share host 工具。

// initStage 创建进程内暂存管理器并接好记账。失败只记日志(降级为无暂存服务),
// 不拖垮启动。
func (s *Server) initStage(dataDir string) {
	m, err := stage.New(s.ctx, filepath.Join(dataDir, "stage"))
	if err != nil {
		log.Printf("[stage] disabled: %v", err)
		return
	}
	m.OnEvent = s.recordStageEvent
	s.stage = m
	log.Printf("[stage] 受管暂存服务已就绪: GET /s/<token>/<name> (TTL 默认 %v,上限 %v)", stage.DefaultTTL, stage.MaxTTL)
}

// SetStageBaseURL 设置拼进暂存 URL 的对外基址(通常是主监听地址)。目标机器能
// 用哪个地址回连平台取决于部署,这里只是给 stage_share 的返回一个可直接用的绝对 URL。
func (s *Server) SetStageBaseURL(addr string) {
	if s.stage == nil {
		return
	}
	host := addr
	if strings.HasPrefix(host, "0.0.0.0") {
		host = strings.Replace(host, "0.0.0.0", "127.0.0.1", 1) // 监听全接口时给回环占位,实际按目标可达地址替换
	}
	s.stage.SetBaseURL("http://" + host)
}

// recordStageEvent 把 stage 记账事件写进归属任务的 activity(参照 engine.emitActivity
// 的 system/text 模式);任务已结束或无任务上下文时至少留服务端日志。
func (s *Server) recordStageEvent(ev stage.Event) {
	e := ev.Entry
	summary := fmt.Sprintf("stage %s: %s (token=%s… size=%d bytes, expires %s)",
		ev.Kind, e.Name, e.Token[:8], e.Size, e.ExpiresAt.Format(time.RFC3339))
	if ev.Kind == stage.EventDownload {
		summary = fmt.Sprintf("stage download: %s 被 %s 拉取 %d/%d 字节(累计命中 %d 次)",
			e.Name, ev.Remote, ev.Bytes, e.Size, e.Hits)
	}
	log.Printf("[stage] task=%s %s", orDash(e.TaskID), summary)
	if e.TaskID == "" {
		return
	}
	s.m.mu.Lock()
	t := s.m.tasks[e.TaskID]
	s.m.mu.Unlock()
	if t == nil || t.Store == nil {
		return // 任务句柄已不在(结束/删除),日志已留
	}
	if _, err := t.Store.AppendActivity(db.Activity{
		Worker: "system", Kind: "text", Tool: "stage_share",
		Summary: summary,
		Detail:  fmt.Sprintf("token=%s name=%s size=%d one_shot=%v expires_at=%s remote=%s bytes=%d hits=%d",
			e.Token, e.Name, e.Size, e.OneShot, e.ExpiresAt.Format(time.RFC3339), ev.Remote, ev.Bytes, e.Hits),
	}); err != nil {
		log.Printf("[stage] activity 写入失败(task=%s): %v", e.TaskID, err)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// stageDownload 服务下载。挂 root mux(JWT 之外);防枚举的一致性 404 与限速
// 都在 stage.Manager.Download 里。
func (s *Server) stageDownload(w http.ResponseWriter, r *http.Request) {
	if s.stage == nil {
		http.NotFound(w, r)
		return
	}
	s.stage.Download(w, r, r.PathValue("token"), r.PathValue("name"))
}

// --- 管理 API(JWT 之后,供 worker 工具与 UI 使用) ---

type stageCreateReq struct {
	Path          string `json:"path"`            // 平台侧已有文件(与 content 二选一)
	Name          string `json:"name"`            // content 模式必填
	ContentBase64 string `json:"content_base64"`  // content 模式
	TTLMinutes    int    `json:"ttl_minutes"`     // 0 = 默认 15 分钟,上限 120
	OneShot       bool   `json:"one_shot"`        // 首次成功完整下载后即焚
	TaskID        string `json:"task_id"`         // 归属任务(activity 记账)
}

func (s *Server) stageCreate(w http.ResponseWriter, r *http.Request) {
	if s.stage == nil {
		writeErr(w, 503, "stage service disabled")
		return
	}
	var req stageCreateReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	opts := stage.PutOpts{
		TTL:     time.Duration(req.TTLMinutes) * time.Minute,
		OneShot: req.OneShot,
		TaskID:  strings.TrimSpace(req.TaskID),
	}
	var e *stage.Entry
	var err error
	switch {
	case strings.TrimSpace(req.Path) != "":
		e, err = s.stage.PutFile(strings.TrimSpace(req.Path), opts)
	case req.ContentBase64 != "":
		data, derr := base64.StdEncoding.DecodeString(req.ContentBase64)
		if derr != nil {
			writeErr(w, 400, "content_base64 解码失败: "+derr.Error())
			return
		}
		e, err = s.stage.PutContent(req.Name, data, opts)
	default:
		writeErr(w, 400, "path 与 content_base64 至少提供一个")
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, e)
}

func (s *Server) stageList(w http.ResponseWriter, r *http.Request) {
	if s.stage == nil {
		writeJSON(w, 200, []stage.Entry{})
		return
	}
	writeJSON(w, 200, s.stage.List())
}

func (s *Server) stageDelete(w http.ResponseWriter, r *http.Request) {
	if s.stage == nil {
		writeErr(w, 503, "stage service disabled")
		return
	}
	if !s.stage.Delete(r.PathValue("token")) {
		writeErr(w, 404, "stage entry not found")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": r.PathValue("token")})
}

// --- stage_share host 工具(默认绑 worker;经 tools 表按 agent 过滤) ---

// stageShareTool 让 worker 把「工作区里已生成的文件」交给受管暂存服务,拿回
// 随机 token URL + 过期时间。TaskID 从 RunInfo 权威注入,不暴露给模型。
func (s *Server) stageShareTool() actool.CoreTool {
	allow := func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}
	return actool.Build(actool.Spec{
		Name: "stage_share",
		Description: "把工作区里已生成的文件交给平台受管暂存服务投递给目标:返回随机 token 下载 URL(/s/<token>/<name>,无目录列举)与过期时间。" +
			"硬 TTL 默认 15 分钟(ttl_minutes 可调,上限 120),到期自动销毁;one_shot=true 时目标首次完整下载后立即即焚。" +
			"【只用于向目标投递工具/payload】——不要投递战利品/凭据,数据回流一律走会话读取/evidence。" +
			"返回的 URL 主机是平台监听地址;目标机器不可达该地址时,用目标可达的平台地址/隧道替换主机部分(路径与 token 不变)。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "要投递的本地文件绝对路径(工作区里已生成的工具/payload)"},
				"ttl_minutes": map[string]any{"type": "integer", "description": "存活分钟数,默认 15,上限 120"},
				"one_shot":    map[string]any{"type": "boolean", "description": "true = 目标首次成功完整下载后立即销毁(默认 false)"},
			},
			"required": []any{"path"},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: allow,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			if s.stage == nil {
				return actool.Errorf("stage 暂存服务未启用"), nil
			}
			var a struct {
				Path       string `json:"path"`
				TTLMinutes int    `json:"ttl_minutes"`
				OneShot    bool   `json:"one_shot"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Path) == "" {
				return actool.Errorf("path 为必填参数"), nil
			}
			ri := agent.RunInfoFrom(ctx)
			taskID := ""
			if ri.TaskID > 0 { // 0 = 非任务 run(聊天会话),无任务上下文
				taskID = strconv.FormatInt(ri.TaskID, 10)
			}
			e, err := s.stage.PutFile(strings.TrimSpace(a.Path), stage.PutOpts{
				TTL:     time.Duration(a.TTLMinutes) * time.Minute,
				OneShot: a.OneShot,
				TaskID:  taskID,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"url":        e.URL,
				"expires_at": e.ExpiresAt.Format(time.RFC3339),
				"one_shot":   e.OneShot,
				"size":       e.Size,
				"note":       "目标机用 curl/wget 拉取该 URL;到期或(开启 one_shot 时)首次完整下载后自动销毁。URL 主机为平台监听地址,目标不可达时按目标可达地址替换主机部分。",
			})
		},
	})
}

// seedStageShareTool 把 stage_share seed 进 tools 表、默认绑定 worker(与流量
// host 工具同一套 SeedTool 首插入语义:老库的用户编辑不被覆盖)。
func (s *Server) seedStageShareTool() {
	t := s.stageShareTool()
	schema, _ := json.Marshal(t.InputSchema())
	workerAgents, _ := json.Marshal([]string{"worker"})
	if err := s.m.PG().SeedTool(t.Name(), t.Description(), schema, workerAgents); err != nil {
		log.Printf("[tools] seed %s 失败: %v", t.Name(), err)
	}
}
