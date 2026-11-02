// Package alert 风控信号告警：把「上游开始收紧」的早期迹象主动推给运维。
//
// 为什么要这个包：本项目的风控证据几乎全部来自「事后翻日志」——11128 通道校验、
// 403 WAF 拦截、12153 session 失效、11140 渠道校验，这些一旦出现就意味着上游已经
// 在收紧，但它们的量通常很小（一天几条到几十条），很容易被淹没在正常请求里。等到
// 变成成片的 400/403 再发现，往往已经晚了。
//
// 设计取舍：
//   - 不做告警引擎。只有「同类事件去重 + 静默窗口 + 一次 HTTP 投递」，规则写死在
//     调用方（server 侧的 noteRiskSignal），这里只负责节流与发送。
//   - 异步发送，绝不阻塞请求路径。发送失败只记日志——告警系统自己不能成为故障源。
//   - 投递格式兼容 PushPlus（body 里的 title/content 是它的字段名），同时也能被任意
//     通用 webhook 消费：PushPlus 把 token 放在 URL query 里即可，其它服务忽略多余字段。
package alert

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// 默认静默窗口：同类事件 30 分钟内只推一次。风控信号往往是「同一件事反复出现」，
// 逐条推送只会让人麻木；30 分钟足够覆盖一轮排查，又不至于漏掉持续恶化。
const defaultMinInterval = 30 * time.Minute

// Notifier 风控信号投递器。零值不可用，用 New 构造；webhook 为空时全部方法无效。
type Notifier struct {
	webhook     string
	minInterval time.Duration
	client      *http.Client

	mu   sync.Mutex
	last map[string]time.Time // kind → 上次投递时刻
}

// New 构造投递器。webhook 为空（或 enabled=false 时调用方不构造）返回 nil，
// 调用方按 nil 安全短路。minInterval <= 0 时用默认 30 分钟。
func New(webhook string, minInterval time.Duration) *Notifier {
	if webhook == "" {
		return nil
	}
	if minInterval <= 0 {
		minInterval = defaultMinInterval
	}
	return &Notifier{
		webhook:     webhook,
		minInterval: minInterval,
		client:      &http.Client{Timeout: 10 * time.Second},
		last:        make(map[string]time.Time),
	}
}

// payload 投递体。title/content 是 PushPlus 的字段名；kind/at 给通用 webhook 用。
type payload struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Kind    string `json:"kind"`
	At      string `json:"at"`
	// Template 仅 PushPlus 消费（markdown 让内容里的换行/代码块正常渲染）；
	// 其它实现忽略未知字段。
	Template string `json:"template"`
}

// Report 上报一次风控信号。kind 是去重键（同类事件在静默窗口内只发一次）；
// title 一句话结论，detail 放账号/模型/上游原文等细节。
//
// 非阻塞：投递在独立 goroutine 里完成。返回 false 表示被静默窗口吞掉（未投递）。
func (n *Notifier) Report(kind, title, detail string) bool {
	if n == nil {
		return false
	}
	now := time.Now()
	n.mu.Lock()
	if t, ok := n.last[kind]; ok && now.Sub(t) < n.minInterval {
		n.mu.Unlock()
		return false
	}
	n.last[kind] = now
	n.mu.Unlock()

	p := payload{
		Title:    title,
		Content:  detail,
		Kind:     kind,
		At:       now.Format("2006-01-02 15:04:05"),
		Template: "markdown",
	}
	go n.send(p)
	return true
}

func (n *Notifier) send(p payload) {
	body, err := json.Marshal(p)
	if err != nil {
		log.Printf("WARN: [alert] marshal %s: %v", p.Kind, err)
		return
	}
	resp, err := n.client.Post(n.webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("WARN: [alert] send %s: %v", p.Kind, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("WARN: [alert] send %s: HTTP %d", p.Kind, resp.StatusCode)
		return
	}
	log.Printf("alert: 已推送风控信号 kind=%s", p.Kind)
}
