package upstream

import (
	"net/http"
	"regexp"
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
	// 会话 id 与轮级请求 id 是 32 hex；Message-ID 走 UUID v4（带连字符）——
	// 官方 applyGatewayHeaders 用 randomUUID() 生成消息 id 与请求 id，与 32 hex 的
	// 轮级聚合主键不是同一形态族，别把三者当成同一种 id。
	for _, h := range []string{"X-Conversation-ID", "X-Conversation-Request-ID"} {
		v := req.Header.Get(h)
		if v == "" {
			t.Fatalf("%s missing (通道校验会拒绝缺少会话三元组的请求)", h)
		}
		if !hex32.MatchString(v) {
			t.Errorf("%s = %q, want 32 hex", h, v)
		}
	}
	if v := req.Header.Get("X-Conversation-Message-ID"); !acpUUIDRe.MatchString(v) {
		t.Errorf("X-Conversation-Message-ID = %q, want UUID v4", v)
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

// acpUUIDRe UUID v4 形态（8-4-4-4-12 小写 hex，版本位 4，variant RFC 4122）。
var acpUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestACPConnectionIDShapeAndStability acp-connection-id 必须是 UUID v4 形态，
// 且「同一会话恒同值、不同会话不同值」——对齐官方 5.9.2 VlmGateway.acquire 里
// 按 sessionId 生成一次并复用的语义。
func TestACPConnectionIDShapeAndStability(t *testing.T) {
	a1 := acpConnectionID("conv-alpha")
	a2 := acpConnectionID("conv-alpha")
	b := acpConnectionID("conv-beta")

	for _, s := range []string{a1, a2, b} {
		if !acpUUIDRe.MatchString(s) {
			t.Errorf("%q 不是 UUID v4 形态", s)
		}
	}
	if a1 != a2 {
		t.Errorf("同一会话应恒同值：%q vs %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("不同会话应不同值，实际都是 %q", a1)
	}
}

// TestChatHeadersCarryACPConnectionID chat 发出的请求必须带 acp-connection-id，
// 且同一 meta.ConversationID 下两次构造得到同一个值。
func TestChatHeadersCarryACPConnectionID(t *testing.T) {
	c := &Client{}
	a := &auth.Auth{UID: "u1", AccessToken: "at", Domain: "www.codebuddy.cn"}
	meta := ChatMeta{ConversationID: "conv-xyz", ConversationRequestID: "0123456789abcdef0123456789abcdef"}

	get := func() string {
		// 每次新建 req（头容器不能复用），但 meta 相同。
		req, _ := http.NewRequest("POST", "https://example.com/v2/chat/completions", nil)
		c.ChatHeaders(req, a, "", meta)
		return req.Header.Get("acp-connection-id")
	}
	first, second := get(), get()
	if first == "" {
		t.Fatal("chat 请求缺 acp-connection-id")
	}
	if first != second {
		t.Errorf("同一会话两次构造应同值：%q vs %q", first, second)
	}
	if !acpUUIDRe.MatchString(first) {
		t.Errorf("%q 不是 UUID v4 形态", first)
	}
}
