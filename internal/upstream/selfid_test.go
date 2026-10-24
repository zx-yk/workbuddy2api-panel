package upstream

import (
	"os"
	"strings"
	"testing"
)

// 自动任务与缓存键是网关自行构造上行标识的两条路径，不得泄露项目前缀或账号明文。
func TestGeneratedOutboundIDsHaveNoSelfIdentifyingPrefix(t *testing.T) {
	for _, path := range []string{"../panel/autotask.go", "cache_key.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, `"wb2api-`) || strings.Contains(line, `"wb2a-`) {
				t.Errorf("%s:%d contains a self-identifying ID prefix", path, i+1)
			}
		}
	}
}
