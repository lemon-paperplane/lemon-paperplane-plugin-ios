package ios

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// xcodebuild 输出过滤：错误连同上下文完整显示，警告默认只在最后报个数。
// 像 Unity 导出的工程一次构建能有几百条 Swift 并发警告，-quiet 也会全部输出，会把真正的错误淹没；
// Xcode 自己也是把警告放在「问题」导航里而不是刷屏。
//
// 经插件运行器代跑：`<插件> __run xcodebuild [--warnings] [--summary 模板] -- <xcodebuild 参数...>`

const runnerXcodebuild = "xcodebuild"

// RunMain 实现 sdk.Runner。
func (p *Plugin) RunMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing runner mode")
		return 2
	}
	switch args[0] {
	case runnerXcodebuild:
		showWarn, summary := false, "{0} warnings hidden"
		rest := args[1:]
		for len(rest) > 0 && rest[0] != "--" {
			switch rest[0] {
			case "--warnings":
				showWarn = true
			case "--summary":
				if len(rest) > 1 {
					summary = rest[1]
					rest = rest[1:]
				}
			}
			rest = rest[1:]
		}
		if len(rest) > 0 {
			rest = rest[1:]
		}
		return runFilteredXcodebuild(rest, showWarn, summary, os.Stdout)
	}
	fmt.Fprintln(os.Stderr, "unknown runner mode:", args[0])
	return 2
}

// filteredXcodebuild 返回经运行器代跑 xcodebuild 的命令、参数和控制台里显示的命令行。
func filteredXcodebuild(ctx *sdk.Context, showWarnings bool, xcArgs []string) (cmd string, args []string, display string) {
	runnerArgs := []string{runnerXcodebuild}
	if showWarnings {
		runnerArgs = append(runnerArgs, "--warnings")
	}
	runnerArgs = append(runnerArgs, "--summary", ctx.T("build.warningsHidden"), "--")
	runnerArgs = append(runnerArgs, xcArgs...)
	cmd, args = sdk.RunnerCommand(runnerArgs...)
	return cmd, args, "xcodebuild " + strings.Join(xcArgs, " ")
}

func runFilteredXcodebuild(xcArgs []string, showWarn bool, summary string, out io.Writer) int {
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	cmd := exec.Command("xcodebuild", xcArgs...)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(out, err)
		return 127
	}
	_ = w.Close()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	f := newXcFilter(out, showWarn)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		f.line(sc.Text())
	}
	err = cmd.Wait()
	signal.Stop(sig)
	close(sig)
	f.finish(summary)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}

var (
	// 行首（不缩进）的诊断：/path/a.swift:12:3: error: … 、clang: error: … 、error: …
	xcErrorRe   = regexp.MustCompile(`^(?:\S[^:]*(?::\d+){0,2}: )?(?:fatal )?error: `)
	xcWarningRe = regexp.MustCompile(`^(?:\S[^:]*(?::\d+){0,2}: )?warning: `)
	xcNoteRe    = regexp.MustCompile(`^(?:\S[^:]*(?::\d+){0,2}: )?note: `)
)

type xcFilter struct {
	out      io.Writer
	showWarn bool
	warnings int
	// 当前诊断后面的缩进上下文（源码摘录、`- 说明）：错误要显示，警告不显示
	showCtx   bool
	ctxLeft   int
	failBlock bool
}

func newXcFilter(out io.Writer, showWarn bool) *xcFilter {
	return &xcFilter{out: out, showWarn: showWarn}
}

func (f *xcFilter) print(s string) { fmt.Fprintln(f.out, s) }

func (f *xcFilter) line(s string) {
	t := strings.TrimSpace(s)
	indented := s != "" && (s[0] == ' ' || s[0] == '\t')
	switch {
	case t == "":
		f.showCtx, f.failBlock = false, false
	case f.failBlock:
		// 「The following build commands failed:」后面的列表全部显示
		f.print(s)
	case t == "The following build commands failed:":
		f.failBlock = true
		f.print(s)
	case !indented && xcErrorRe.MatchString(s):
		f.showCtx, f.ctxLeft = true, 20
		f.print(s)
	case !indented && xcWarningRe.MatchString(s):
		f.warnings++
		f.showCtx, f.ctxLeft = f.showWarn, 20
		if f.showWarn {
			f.print(s)
		}
	case !indented && xcNoteRe.MatchString(s):
		// 附属于上一条诊断的说明，跟着上一条显示或隐藏
		if f.showCtx {
			f.print(s)
		}
	case indented:
		if f.showCtx && f.ctxLeft > 0 {
			f.ctxLeft--
			f.print(s)
		}
	default:
		f.showCtx = false
		f.print(s)
	}
}

func (f *xcFilter) finish(summary string) {
	if f.warnings > 0 && !f.showWarn {
		f.print(strings.ReplaceAll(summary, "{0}", strconv.Itoa(f.warnings)))
	}
}
