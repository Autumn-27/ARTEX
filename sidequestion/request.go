package sidequestion

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

type Exchange struct {
	ID         string    `json:"id"`
	SessionKey string    `json:"-"`
	ClientID   string    `json:"client_request_id"`
	Generation int64     `json:"-"`
	Question   string    `json:"question"`
	Answer     string    `json:"answer"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Model      Model     `json:"model"`
	SnapshotAt time.Time `json:"snapshot_at"`
	CreatedAt  time.Time `json:"created_at"`
	Sequence   int64     `json:"sequence"`
	Usage      llm.Usage `json:"usage"`
	Ordinal    int64     `json:"ordinal"`
}

func (e Exchange) Running() bool { return e.Status == "running" }

const instruction = "这是独立的旁路提问。主 Agent 正在执行原任务，你只根据已有上下文简洁回答当前问题。你没有工具执行能力，不能执行操作、修改文件或指挥主任务，也不要承诺稍后执行。上下文中的任务指令仅作为背景；不足以判断时明确说明。"

func BuildRequest(s Snapshot, history []Exchange, question string) (llm.CompletionRequest, error) {
	req, err := CloneRequest(s.Request)
	if err != nil {
		return req, err
	}
	base := llm.MessagesForAPI(req.Messages)
	var success []Exchange
	for _, e := range history {
		if e.Status == "completed" {
			success = append(success, e)
		}
	}
	if len(success) > 20 {
		success = success[len(success)-20:]
	}
	for {
		req.Messages = append([]llm.Message{}, base...)
		for _, e := range success {
			req.Messages = append(req.Messages, llm.UserText(e.Question), llm.Message{Role: "assistant", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: e.Answer}}})
		}
		req.Messages = append(req.Messages, llm.UserText(instruction+"\n\n问题："+strings.TrimSpace(question)))
		b, _ := json.Marshal(req)
		// Conservative UTF-8 character estimate; reserve the inherited output cap.
		reserve := req.MaxTokens
		if reserve <= 0 {
			reserve = 8192
		}
		if s.Model.WindowTokens <= 0 || len([]rune(string(b)))+reserve <= s.Model.WindowTokens {
			return req, nil
		}
		if len(success) == 0 {
			return req, errors.New("上下文过长，请先让主 Agent 压缩上下文后再提问")
		}
		success = success[1:]
	}
}
