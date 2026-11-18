package ios

import (
	"bytes"
	"strings"
	"testing"
)

const xcSample = `/tmp/App/Sources/Video.swift:96:18: warning: call to main actor-isolated instance method 'failed(_:)' in a synchronous nonisolated context
 94 |             let failure = note.userInfo?["k"] as? Error
 96 |             self.failed(failure)
    |                  ` + "`" + `- warning: call to main actor-isolated instance method 'failed(_:)'
/tmp/App/Sources/Video.swift:250:18: note: calls to instance method 'failed(_:)' from outside of its actor context are implicitly asynchronous
/tmp/App/Sources/Main.swift:12:5: error: cannot find 'foo' in scope
 12 |     foo()
    |     ` + "`" + `- error: cannot find 'foo' in scope
/tmp/App/Sources/Log.swift:9:41: warning: conformance crosses into main actor-isolated code

The following build commands failed:
	SwiftCompile normal arm64 /tmp/App/Sources/Main.swift (in target 'App' from project 'App')
(1 failure)
** BUILD FAILED **
`

func runFilter(showWarn bool) string {
	var b bytes.Buffer
	f := newXcFilter(&b, showWarn)
	for _, l := range strings.Split(xcSample, "\n") {
		f.line(l)
	}
	f.finish("另有 {0} 条编译警告未显示")
	return b.String()
}

func TestXcFilterHidesWarningsKeepsErrors(t *testing.T) {
	out := runFilter(false)
	for _, want := range []string{"Main.swift:12:5: error: cannot find 'foo'", "    foo()", "- error: cannot find 'foo'",
		"The following build commands failed:", "SwiftCompile normal arm64", "** BUILD FAILED **", "另有 2 条编译警告未显示"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"warning:", "note: calls", "self.failed"} {
		if strings.Contains(out, bad) {
			t.Fatalf("warning context %q should be hidden:\n%s", bad, out)
		}
	}
}

func TestXcFilterShowWarnings(t *testing.T) {
	out := runFilter(true)
	if !strings.Contains(out, "Video.swift:96:18: warning:") || !strings.Contains(out, "note: calls") || strings.Contains(out, "未显示") {
		t.Fatalf("warnings should be shown:\n%s", out)
	}
}
