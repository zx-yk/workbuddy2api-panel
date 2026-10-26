package upstream

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestEffortOffDisablesThinking 客户端用档位字段表达关闭（off/none/disabled）时，
// deepseek 必须转成 thinking.type=disabled 并清掉档位字段。
//
// 不截住就会被降级管线按 rank 0 就近抬成**最弱正档**——用户要"关"，拿到的是
// "开着但最弱"（实测 effort=off 仍返回 157 字思维链，与 high 的 159 字无异）。
func TestEffortOffDisablesThinking(t *testing.T) {
	for _, v := range []string{"off", "none", "disabled", "OFF", " None "} {
		for _, field := range []string{"reasoning_effort", "reasoningEffort"} {
			obj := map[string]any{"model": "deepseek-v4.1-flash", field: v}
			injectThinking(obj, "high")
			th, ok := obj["thinking"].(map[string]any)
			if !ok || th["type"] != "disabled" {
				t.Fatalf("%s=%q → thinking=%v want type=disabled", field, v, obj["thinking"])
			}
			if _, has := obj["reasoning_effort"]; has {
				t.Fatalf("%s=%q 关思考时不得保留 reasoning_effort", field, v)
			}
			if _, has := obj["reasoningEffort"]; has {
				t.Fatalf("%s=%q 关思考时不得保留 reasoningEffort", field, v)
			}
		}
	}
}

// TestEffortOffKeepsExplicitEnabled 显式 thinking.type=enabled 且带 off 档位时，
// off 仍表达"关"，以 disabled 为准（档位字段是客户端的直接意图）。
func TestEffortOffKeepsExplicitEnabled(t *testing.T) {
	obj := map[string]any{
		"model":            "deepseek-v4-pro",
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "off",
	}
	injectThinking(obj, "high")
	th := obj["thinking"].(map[string]any)
	if th["type"] != "disabled" {
		t.Fatalf("thinking.type=%v want disabled", th["type"])
	}
}

// TestEffortOffDoesNotTouchOtherModels 非 deepseek 模型保持零改动（关思考语义不同）。
func TestEffortOffDoesNotTouchOtherModels(t *testing.T) {
	obj := map[string]any{"model": "glm-5.2", "reasoning_effort": "off"}
	injectThinking(obj, "high")
	if _, ok := obj["thinking"]; ok {
		t.Fatalf("非 deepseek 不应注入 thinking: %v", obj["thinking"])
	}
	if obj["reasoning_effort"] != "off" {
		t.Fatalf("非 deepseek 不应改写档位: %v", obj["reasoning_effort"])
	}
}

// TestDisableThinkingBody 救援用的请求体副本：同额度、关思考、清档位；非 deepseek 放弃。
func TestDisableThinkingBody(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash","max_tokens":200,"stream":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	out, ok := DisableThinkingBody(body)
	if !ok {
		t.Fatal("deepseek 请求应可构造关思考副本")
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("副本非合法 JSON: %v", err)
	}
	th, _ := obj["thinking"].(map[string]any)
	if th["type"] != "disabled" {
		t.Fatalf("thinking=%v want disabled", obj["thinking"])
	}
	if _, has := obj["reasoning_effort"]; has {
		t.Fatal("副本应清掉 reasoning_effort")
	}
	// 额度不放大：客户端的 max_tokens 原样保留（超发既是语义违约也是新的可判别形态）。
	if mt, _ := obj["max_tokens"].(float64); mt != 200 {
		t.Fatalf("max_tokens=%v want 200（不得放大客户端上限）", obj["max_tokens"])
	}
	if _, has := obj["stream"]; !has {
		t.Fatal("流式标志应保留")
	}

	if _, ok := DisableThinkingBody([]byte(`{"model":"glm-5.2"}`)); ok {
		t.Fatal("非 deepseek 不应构造救援副本")
	}
	if _, ok := DisableThinkingBody([]byte(`not json`)); ok {
		t.Fatal("非法 JSON 不应构造救援副本")
	}
}

// TestEmptyTruncatedReply 只认「finish_reason=length + 正文零字节 + 无 tool_calls」。
func TestEmptyTruncatedReply(t *testing.T) {
	cases := []struct {
		name string
		resp string
		want bool
	}{
		{"空正文被截断", `{"choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":""}}]}`, true},
		{"有正文的截断", `{"choices":[{"index":0,"finish_reason":"length","message":{"content":"partial"}}]}`, false},
		{"正常收尾", `{"choices":[{"index":0,"finish_reason":"stop","message":{"content":""}}]}`, false},
		{"带工具调用", `{"choices":[{"index":0,"finish_reason":"length","message":{"content":"","tool_calls":[{"id":"x"}]}}]}`, false},
		{"无 choices", `{}`, false},
	}
	for _, c := range cases {
		var obj map[string]any
		if err := json.Unmarshal([]byte(c.resp), &obj); err != nil {
			t.Fatalf("%s: bad json: %v", c.name, err)
		}
		if got := EmptyTruncatedReply(obj); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// TestStreamRescueReplacesEmptyReply 空回复（finish=length、正文零字节）时换成关思考
// 重发的流：客户端只看到一份正常回复，且不会看到第一跳的 finish_reason=length。
func TestStreamRescueReplacesEmptyReply(t *testing.T) {
	empty := strings.Join([]string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	fixed := strings.Join([]string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"你好"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	rec := httptest.NewRecorder()
	calls := 0
	err := StreamHint(rec, strings.NewReader(empty), nil, WithEmptyReplyRescue(func() (io.Reader, func(), error) {
		calls++
		return strings.NewReader(fixed), nil, nil
	}))
	if err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if calls != 1 {
		t.Fatalf("救援回调次数=%d want 1", calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "你好") {
		t.Fatalf("应输出救援流的正文，实际 body=%q", body)
	}
	if strings.Contains(body, `"length"`) {
		t.Fatalf("第一跳的空 finish_reason=length 不应发给客户端，body=%q", body)
	}
	// 第一跳的 role 帧被丢弃，只剩下救援流自己的一帧开头帧（不得重复）。
	if n := strings.Count(body, `"role":"assistant"`); n != 1 {
		t.Fatalf("role 帧次数=%d want 1（第一跳的元数据帧应被丢弃），body=%q", n, body)
	}
	if n := strings.Count(body, "[DONE]"); n != 1 {
		t.Fatalf("[DONE] 次数=%d want 1，body=%q", n, body)
	}
}

// TestStreamRescueSkippedWhenContentExists 有正文时绝不触发救援，且帧顺序与未接线一致。
func TestStreamRescueSkippedWhenContentExists(t *testing.T) {
	normal := strings.Join([]string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	rec := httptest.NewRecorder()
	err := StreamHint(rec, strings.NewReader(normal), nil, WithEmptyReplyRescue(func() (io.Reader, func(), error) {
		t.Fatal("有正文的截断是客户端额度语义的正常结果，不应救援")
		return nil, nil, nil
	}))
	if err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hi") || !strings.Contains(body, `"length"`) {
		t.Fatalf("未救援时应原样透传，body=%q", body)
	}
	// 缓冲不得改变顺序：role 帧必须在 content 帧之前。
	if strings.Index(body, `"role":"assistant"`) > strings.Index(body, `"content":"hi"`) {
		t.Fatalf("帧顺序被打乱: %q", body)
	}
}

// TestBareErrorFrameDetectedAndObserved 顶层 {code,msg}（无 error 无 choices）是上游
// 11140 审核 / 6021 队列满的实测失败形态：必须按错误帧处置，不能当正常帧透传并记成功。
func TestBareErrorFrameDetectedAndObserved(t *testing.T) {
	payload := `{"code":11140,"msg":"request illegal","requestId":"r1"}`
	stream := strings.Join([]string{
		"data: " + payload,
		`data: [DONE]`,
		``,
	}, "\n\n")

	rec := httptest.NewRecorder()
	var got string
	err := StreamHint(rec, strings.NewReader(stream), nil,
		WithErrorFrameObserver(func(p string) { got = p }))
	if err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if got != payload {
		t.Fatalf("观察者未收到失败帧原文: %q", got)
	}
	if !strings.Contains(rec.Body.String(), `11140`) {
		t.Fatalf("失败帧应透传给客户端，body=%q", rec.Body.String())
	}
}

// TestNormalFramesNotTreatedAsBareError 带 choices 的帧即使有 code 字段也不是失败帧。
func TestNormalFramesNotTreatedAsBareError(t *testing.T) {
	payload := `{"code":0,"choices":[{"index":0,"delta":{"content":"hi"}}]}`
	stream := strings.Join([]string{"data: " + payload, `data: [DONE]`, ``}, "\n\n")

	rec := httptest.NewRecorder()
	var got string
	err := StreamHint(rec, strings.NewReader(stream), nil,
		WithErrorFrameObserver(func(p string) { got = p }))
	if err != nil {
		t.Fatalf("StreamHint: %v", err)
	}
	if got != "" {
		t.Fatalf("正常帧被误判为失败帧: %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"hi"`) {
		t.Fatalf("正文应正常透传: %q", rec.Body.String())
	}
}

// TestSessionDeadButUsable AT 仍是唯一稳定判据：nil/过期 → false，未过期 → true。
func TestSessionDeadButUsable(t *testing.T) {
	if SessionDeadButUsable(nil) {
		t.Fatal("nil auth 不应判可用")
	}
	if SessionDeadButUsable(&auth.Auth{UID: "u", ExpiresAt: 1}) {
		t.Fatal("AT 已过期不应判可用")
	}
	if SessionDeadButUsable(&auth.Auth{UID: "u"}) {
		t.Fatal("ExpiresAt 缺省（0）不应判可用")
	}
	if !SessionDeadButUsable(&auth.Auth{UID: "u", ExpiresAt: time.Now().Add(time.Hour).Unix()}) {
		t.Fatal("AT 未过期应判可用")
	}
}
