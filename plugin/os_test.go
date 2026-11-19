package ios

import (
	"os"
	"runtime"
	"testing"
)

// iOS 插件只在 macOS 上加载（plugin.json 的 os），测试也只在 macOS 上跑：里面都是 Xcode 的 Unix 路径。
func TestMain(m *testing.M) {
	if runtime.GOOS != "darwin" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
