package upstream

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatHeadersAlwaysSendConversationTrio 回归 issue #190：国际端点收紧通道
// 校验后，缺 X-Conversation-ID 的聊天请求会 400 / 11128（conversation id
// missing）。即使调用方没给会话 ID（零值 ChatMeta），三元组也必须齐全，
// 且兜底 ID 是 32 位 hex。
func TestChatHeadersAlwaysSendConversationTrio(t *testing.T) {
	c := &Client{}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.ChatHeaders(req, testAuth(), "", ChatMeta{})

	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	// 会话 id、轮级请求 id、消息级 id 全是 32 位小写 hex。消息级两个头
	// （X-Conversation-Message-ID 与 X-Request-ID）**同值**——CLI 内核里是
	// `q = randomUUID().replace(/-/g,"")`，两个头都吃同一个 q。
	// v1.16.0 曾按 app.asar 的 applyGatewayHeaders 改成两个独立 UUID v4，那是错层
	// （那是 UI→CLI 的 ACP 元数据层，chat 由 CLI 内核发出）。
	for _, h := range []string{"X-Conversation-ID", "X-Conversation-Request-ID"} {
		v := req.Header.Get(h)
		if v == "" {
			t.Fatalf("%s missing (通道校验会拒绝缺少会话三元组的请求)", h)
		}
		if !hex32.MatchString(v) {
			t.Errorf("%s = %q, want 32 hex", h, v)
		}
	}
	msgID := req.Header.Get("X-Conversation-Message-ID")
	if !hex32.MatchString(msgID) {
		t.Errorf("X-Conversation-Message-ID = %q, want 32 hex", msgID)
	}
	if got := req.Header.Get("X-Request-ID"); got != msgID {
		t.Errorf("X-Request-ID=%q 应与 X-Conversation-Message-ID=%q 同值", got, msgID)
	}
	if v := req.Header.Get("X-Root-Request-ID"); v == "" {
		t.Error("X-Root-Request-ID 缺失（CLI 主对话会发 = conversationRequestID）")
	}

	// 兜底会话 ID 每次请求新生成（不是常量），且与 request/message ID 不冲突。
	req2, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	c.ChatHeaders(req2, testAuth(), "", ChatMeta{})
	if req.Header.Get("X-Conversation-ID") == req2.Header.Get("X-Conversation-ID") {
		t.Error("fallback X-Conversation-ID should be fresh per request")
	}
}

// TestChatHeadersPreservesConversationID 入站带来的会话 ID 优先透传，不被兜底覆盖。
func TestChatHeadersPreservesConversationID(t *testing.T) {
	c := &Client{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	const conv = "0123456789abcdef0123456789abcdef"
	c.ChatHeaders(req, testAuth(), "", ChatMeta{ConversationID: conv})
	if got := req.Header.Get("X-Conversation-ID"); got != conv {
		t.Errorf("X-Conversation-ID = %q, want supplied %q", got, conv)
	}
}

// testAuth 返回最小的 CN 账号（ChatHeaders 会读 UID/凭证，nil 会解引用）。
func testAuth() *auth.Auth {
	return &auth.Auth{UID: "u1", AccessToken: "at"}
}

// uuidShapeRe UUID 形态（8-4-4-4-12 小写 hex，不校验版本位）。
var uuidShapeRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestChatHeadersNoACPConnectionID acp-connection-id 不该出现在 chat 请求里。
//
// 2026-10-28 抓官方 CLI 2.165.0 实测：chat 40/41 个头里**没有**它；CLI 源码里这个
// 名字的 30 处引用全在本地 ACP 控制面（服务端校验 / CORS 白名单 / /api/v1/acp 客户端），
// 没有一处加到上游请求。v1.16.0 按 app.asar 的 ACP 层加进来是错层。
func TestChatHeadersNoACPConnectionID(t *testing.T) {
	c := &Client{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	c.ChatHeaders(req, testAuth(), "", ChatMeta{ConversationID: "d-0123456789abcdef0123456789abcdef"})
	if v := req.Header.Get("acp-connection-id"); v != "" {
		t.Errorf("chat 请求不该带 acp-connection-id，实际 %q", v)
	}
}

// TestChatHeadersTraceFamilyCoherent 链路族必须是一个自洽的 OTEL trace：
// traceparent 的 trace-id == b3 首段 == X-Trace-ID == X-B3-TraceId；span id 每请求新、
// 与 message id 无关；parent span 随会话稳定。
func TestChatHeadersTraceFamilyCoherent(t *testing.T) {
	c := &Client{}
	meta := ChatMeta{ConversationID: "d-0123456789abcdef0123456789abcdef",
		ConversationRequestID: "fedcba9876543210fedcba9876543210"}

	get := func() http.Header {
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
		c.ChatHeaders(req, testAuth(), "", meta)
		return req.Header
	}
	h1, h2 := get(), get()

	trace := h1.Get("X-B3-TraceId")
	if len(trace) != 32 {
		t.Fatalf("X-B3-TraceId=%q 应为 32 hex", trace)
	}
	if h1.Get("X-Trace-ID") != trace {
		t.Errorf("X-Trace-ID=%q 应等于 X-B3-TraceId=%q", h1.Get("X-Trace-ID"), trace)
	}
	tp := h1.Get("traceparent")
	if want := "00-" + trace + "-"; !strings.HasPrefix(tp, want) || !strings.HasSuffix(tp, "-01") {
		t.Errorf("traceparent=%q 与 trace id %q 不自洽", tp, trace)
	}
	b3 := h1.Get("b3")
	if !strings.HasPrefix(b3, trace+"-") || !strings.HasSuffix(b3, "-1-"+h1.Get("X-B3-ParentSpanId")) {
		t.Errorf("b3=%q 与 trace/parent 不自洽", b3)
	}
	span1 := h1.Get("X-B3-SpanId")
	if len(span1) != 16 {
		t.Errorf("SpanId=%q 应为 16 hex", span1)
	}
	if span1 != h2.Get("X-B3-SpanId") {
		// 期望每请求新 span；不等即正确
	} else if span1 != "" {
		t.Errorf("SpanId 每请求应不同，两次都是 %q", span1)
	}
	if h1.Get("X-B3-ParentSpanId") != h2.Get("X-B3-ParentSpanId") {
		t.Error("同一会话的 parent span 应稳定")
	}
	if h1.Get("X-B3-Sampled") != "1" {
		t.Errorf("X-B3-Sampled=%q want 1", h1.Get("X-B3-Sampled"))
	}
	// span 与消息 id 无关（此前 SpanId 直接取 messageID[:16]）
	if span1 == h1.Get("X-Conversation-Message-ID")[:16] {
		t.Error("SpanId 不该等于 message id 前 16 位")
	}
}

// TestChatHeadersBaggage 官方把会话 id 与轮级请求 id 作为 W3C baggage 双发。
func TestChatHeadersBaggage(t *testing.T) {
	c := &Client{}
	const convReq = "fedcba9876543210fedcba9876543210"
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	c.ChatHeaders(req, testAuth(), "", ChatMeta{
		ConversationID:        "d-0123456789abcdef0123456789abcdef",
		ConversationRequestID: convReq,
	})
	want := "codebuddy.session_id=01234567-89ab-cdef-0123-456789abcdef,codebuddy.conversation_request_id=" + convReq
	if got := req.Header.Get("baggage"); got != want {
		t.Errorf("baggage=%q\n want %q", got, want)
	}
}

// TestChatHeadersSDKStamp 官方 js SDK 的 7 个 x-stainless-* 头必须齐全且取值固定
// （2026-10-28 抓包实测值）。
func TestChatHeadersSDKStamp(t *testing.T) {
	c := &Client{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	c.ChatHeaders(req, testAuth(), "", ChatMeta{})
	want := map[string]string{
		"x-stainless-arch":            "x64",
		"x-stainless-lang":            "js",
		"x-stainless-os":              "Windows",
		"x-stainless-package-version": "6.25.0",
		"x-stainless-retry-count":     "0",
		"x-stainless-runtime":         "node",
		"x-stainless-runtime-version": "v22.22.2",
	}
	for k, v := range want {
		if got := req.Header.Get(k); got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
}

// TestConversationHeaderIDShape 内部派生键 d-<32hex> 发头时排成 UUID 形态；
// 客户端透传的原值不改。
func TestConversationHeaderIDShape(t *testing.T) {
	if got := conversationHeaderID("d-0123456789abcdef0123456789abcdef"); got != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Errorf("派生键应转 UUID 形态，得到 %q", got)
	}
	for _, raw := range []string{"01a18013-46ce-7483-bddd-2e90d2c24a59", "0123456789abcdef0123456789abcdef"} {
		if got := conversationHeaderID(raw); got != raw {
			t.Errorf("透传值不该被改：%q → %q", raw, got)
		}
	}
	c := &Client{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v2/chat/completions", nil)
	c.ChatHeaders(req, testAuth(), "", ChatMeta{ConversationID: "d-0123456789abcdef0123456789abcdef"})
	if got := req.Header.Get("X-Conversation-ID"); !uuidShapeRe.MatchString(got) {
		t.Errorf("X-Conversation-ID=%q 应为 UUID 形态", got)
	}
}
