package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestModelBlockTTLShortEnoughToSelfHeal 11102/11158 负缓存的 TTL 必须短到能自愈。
//
// 回归背景：11102「service info not found」不总是「这个号真没有这个模型」——路径级差异、
// 上游瞬时抖动、分类误判都会给同一个码，实测同一账号两分钟就恢复。旧参数是 6h 基数 /
// 24h 上限，一次误判就把 (账号,模型) 锁死半天，几个号先后误判就是该模型整池不可用。
//
// 四条区间断言（微调到 3m/12m 也不会红），并钉住「阶梯仍能走到封顶」。
func TestModelBlockTTLShortEnoughToSelfHeal(t *testing.T) {
	if modelBlockBaseTTL > 3*time.Minute {
		t.Errorf("modelBlockBaseTTL=%v 太长（上限 3m）", modelBlockBaseTTL)
	}
	if modelBlockMaxTTL > 12*time.Minute {
		t.Errorf("modelBlockMaxTTL=%v 太长（上限 12m）", modelBlockMaxTTL)
	}
	if modelBlockBaseTTL >= modelBlockMaxTTL {
		t.Errorf("基数 %v 必须小于封顶 %v", modelBlockBaseTTL, modelBlockMaxTTL)
	}
	if got := modelBlockBaseTTL * (1 << modelBlockShift); got <= modelBlockMaxTTL {
		t.Errorf("阶梯末级 %v 未超过封顶 %v（shift=%d 失效）", got, modelBlockMaxTTL, modelBlockShift)
	}

	// 首次命中写 2 分钟左右的 Until，而不是 6 小时。
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.BlockModelBackoff("u1", "m1", "11102 model [m1] service info not found")
	p.mu.Lock()
	until := p.byUID["u1"].modelCooldowns["m1"].Until
	p.mu.Unlock()
	if d := time.Until(until); d > modelBlockBaseTTL+time.Minute {
		t.Fatalf("首次命中 TTL=%v want ≤ %v", d, modelBlockBaseTTL)
	}
}

// TestModelCooldownsRestoreClampsStaleBackoff 存量条目钳制：升级前的 11102 条目可能带着
// 6h/24h 的 Until，不改的话「上线即收窄」要等它们自然到期（最长一天）。6004 是上游重置
// 墙钟语义，绝不能被钳。
func TestModelCooldownsRestoreClampsStaleBackoff(t *testing.T) {
	now := time.Now()
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	// 11102 退避条目（本次会写 ~2m，稍后篡改成旧版本的 6h）。
	p.BlockModelBackoff("u1", "m-backoff", "11102 model [m-backoff] service info not found")
	// 6004 条目：上游墙钟 6 小时后。
	p.CooldownSoftForModel("u1", time.Minute, now.Add(6*time.Hour), "m-quota", "6004 model rate limit")

	p.mu.Lock()
	sf := p.stateOverviewLocked()
	stale := sf.Accounts["u1"].ModelCooldowns["m-backoff"]
	stale.Until = now.Add(6 * time.Hour) // 模拟旧版本落盘的 6h 基数
	sf.Accounts["u1"].ModelCooldowns["m-backoff"] = stale
	p.mu.Unlock()

	p2 := New("")
	p2.Add(&auth.Auth{UID: "u1"})
	p2.mu.Lock()
	p2.applyAccountsLocked(sf.Accounts)
	restored := make(map[string]time.Time, 2)
	for m, mc := range p2.byUID["u1"].modelCooldowns {
		restored[m] = mc.Until
	}
	p2.mu.Unlock()

	if d := restored["m-backoff"].Sub(now); d > modelBlockMaxTTL+time.Minute {
		t.Fatalf("存量 11102 未被钳制：还剩 %v（上限 %v）", d, modelBlockMaxTTL)
	}
	if d := restored["m-quota"].Sub(now); d < 5*time.Hour {
		t.Fatalf("6004 墙钟被误钳：只剩 %v（应保持 ~6h）", d)
	}
}
