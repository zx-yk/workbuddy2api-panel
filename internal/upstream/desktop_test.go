package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// wantDesktopUA 默认配置下的桌面 UA（版本源统一后由 clientVersion/cliVersion 拼出，
// 不再是硬编码常量；测试用它做期望值，避免把某一具体版本号写死两份）。
var wantDesktopUA = (&Client{}).desktopUA()

// TestReportDesktopEventFingerprint 断言桌面指纹上报：走 chatBase、UA 为桌面形状、
// 事件数组自动注入 workbuddy-desktop 公共指纹（覆盖同名业务键）。
func TestReportDesktopEventFingerprint(t *testing.T) {
	var got []map[string]any
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			t.Errorf("path=%s want /v2/report", r.URL.Path)
		}
		gotUA = r.Header.Get("User-Agent")
		if r.Header.Get("X-User-Id") != "u-dt" {
			t.Errorf("X-User-Id=%q want u-dt", r.Header.Get("X-User-Id"))
		}
		if r.Header.Get("X-Product") != "SaaS" {
			t.Errorf("X-Product=%q want SaaS", r.Header.Get("X-Product"))
		}
		// X-Request-ID 恒 32 位小写 hex（官方客户端形态）。此前是
		// deriveID(36hex)+纳秒后 6 位的 42 字符混合串——长度和字符集都偏离，
		// 且与同批 DesktopChatWithExpert 的纯 19 位纳秒数字并存。
		if id := r.Header.Get("X-Request-ID"); !isLowerHexN(id, 32) {
			t.Errorf("X-Request-ID=%q 不是 32 位小写 hex（官方形态）", id)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("body must be array: %v", err)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	err := c.ReportDesktopEvent(&auth.Auth{AccessToken: "at", UID: "u-dt", Nickname: "RenJie"},
		DesktopEvent{"eventCode": "agent_task_created", "mode": "craft"})
	if err != nil {
		t.Fatalf("report desktop: %v", err)
	}
	if gotUA != wantDesktopUA {
		t.Errorf("UA=%q want %q", gotUA, wantDesktopUA)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d want 1", len(got))
	}
	ev := got[0]
	if ev["extName"] != "workbuddy-desktop" {
		t.Errorf("extName=%v want workbuddy-desktop", ev["extName"])
	}
	if ev["ideType"] != "WorkBuddy" || ev["ideName"] != "WorkBuddy" {
		t.Errorf("ideType/ideName=%v/%v want WorkBuddy", ev["ideType"], ev["ideName"])
	}
	if ev["userId"] != "u-dt" {
		t.Errorf("userId=%v want u-dt", ev["userId"])
	}
	if ev["userNickname"] != "RenJie" {
		t.Errorf("userNickname=%v want RenJie", ev["userNickname"])
	}
	if ev["mode"] != "craft" {
		t.Errorf("业务字段 mode=%v 被指纹覆盖或缺失", ev["mode"])
	}
	// machineId 应为 uid 稳定派生（36 hex）。
	if len(ev["machineId"].(string)) != 36 {
		t.Errorf("machineId 长度=%d want 36", len(ev["machineId"].(string)))
	}
}

// TestDesktopChatSequenceShape 断言完整对话事件链的事件码顺序与关键字段。
func TestDesktopChatSequenceShape(t *testing.T) {
	events := DesktopChatSequence("conv-1", "req-1", "msg-1", "fast-model", "fast-model")
	wantCodes := []string{
		"agent_task_created", "chat_message_send", "chat_request_send",
		"chat_message_response", "chat_message_status", "chat_request_response",
	}
	if len(events) != len(wantCodes) {
		t.Fatalf("events=%d want %d", len(events), len(wantCodes))
	}
	for i, want := range wantCodes {
		if events[i]["eventCode"] != want {
			t.Errorf("events[%d].eventCode=%v want %v", i, events[i]["eventCode"], want)
		}
	}
	resp := events[3]
	if resp["isSuccessful"] != true {
		t.Errorf("chat_message_response.isSuccessful=%v want true（RichMeow 判据）", resp["isSuccessful"])
	}
	if resp["finishReason"] != "stop" {
		t.Errorf("finishReason=%v want stop", resp["finishReason"])
	}
	if resp["conversationId"] != "conv-1" {
		t.Errorf("conversationId=%v want conv-1", resp["conversationId"])
	}
}

// TestSetAppearanceTheme 断言主题设置端点形状（点亮 Hp_Appearance 的 API）。
func TestSetAppearanceTheme(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/user-asset/appearance/set" {
			t.Errorf("path=%s want /v2/user-asset/appearance/set", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != wantDesktopUA {
			t.Errorf("UA=%q want desktop UA", r.Header.Get("User-Agent"))
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	if err := c.SetAppearanceTheme(&auth.Auth{AccessToken: "at", UID: "u1"}, "theme-tkmw7j"); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if got["kind"] != "theme" || got["resource_key"] != "theme-tkmw7j" {
		t.Errorf("body=%v want kind=theme resource_key=theme-tkmw7j", got)
	}
}

// isLowerHexN 判定 s 是否为 n 位小写 hex——官方客户端所有 ID 的固定形态
// （X-Request-ID / X-Conversation-* 恒 32 位，设备标识 36/40 位）。
func isLowerHexN(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// TestDesktopChatWithExpertRequestIDShape 专家对话通道（DesktopChatWithExpert）的
// X-Request-ID 也必须是 32 位小写 hex。此前这里是纯 19 位纳秒数字，而桌面上报通道
// 是 36hex+纳秒后 6 位的 42 字符混合串——同一客户端两条通道各发一种非官方形态。
// 顺带钉住这条路径本来就带的 X-Agent-Intent/X-Agent-Type（主 chat 路径还缺）。
func TestDesktopChatWithExpertRequestIDShape(t *testing.T) {
	var gotID, gotIntent, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/chat/completions" {
			t.Errorf("path=%s want /v2/chat/completions", r.URL.Path)
		}
		gotID = r.Header.Get("X-Request-ID")
		gotIntent = r.Header.Get("X-Agent-Intent")
		gotType = r.Header.Get("X-Agent-Type")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"cmb-0123456789abcdef0123456789abcdef\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"2\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	_, reqID, err := c.DesktopChatWithExpert(&auth.Auth{UID: "u-dt", AccessToken: "at", ExpiresAt: 9999999999}, "ex_test")
	if err != nil {
		t.Fatalf("DesktopChatWithExpert: %v", err)
	}
	if !isLowerHexN(gotID, 32) {
		t.Errorf("X-Request-ID=%q 不是 32 位小写 hex（官方形态）", gotID)
	}
	if reqID != "cmb-0123456789abcdef0123456789abcdef" {
		t.Errorf("解析出的服务端 requestId=%q want cmb-0123…", reqID)
	}
	if gotIntent != "craft" {
		t.Errorf("X-Agent-Intent=%q want craft", gotIntent)
	}
	if gotType != "main" {
		t.Errorf("X-Agent-Type=%q want main", gotType)
	}
}

// TestDesktopHardwarePerAccount 硬件参数按账号派生：同账号跨调用稳定、账号间分散。
//
// 此前 cpuCores/memorySize/osVersion 是全局硬编码（20 核 / 24GB / 10.0.26220），
// 多个账号向同一后台汇报同一台机器——这是比「参数真假」更直接的多账号关联信号。
func TestDesktopHardwarePerAccount(t *testing.T) {
	c1, m1, o1 := desktopHardware(&auth.Auth{UID: "u-alpha"})
	c2, m2, o2 := desktopHardware(&auth.Auth{UID: "u-alpha"})
	if c1 != c2 || m1 != m2 || o1 != o2 {
		t.Errorf("同账号应稳定：%d/%d/%s vs %d/%d/%s", c1, m1, o1, c2, m2, o2)
	}

	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		c, m, o := desktopHardware(&auth.Auth{UID: fmt.Sprintf("uid-%d", i)})
		seen[fmt.Sprintf("%d/%d/%s", c, m, o)] = true
		if c < 2 || m < 4 {
			t.Errorf("派生出的参数不合理：cpu=%d mem=%d", c, m)
		}
		if !strings.HasPrefix(o, "10.0.") {
			t.Errorf("osVersion=%q 不是 Windows 10/11 build 形态", o)
		}
	}
	if len(seen) < 2 {
		t.Errorf("12 个账号只出现 %d 种硬件组合，派生没起作用", len(seen))
	}
}
