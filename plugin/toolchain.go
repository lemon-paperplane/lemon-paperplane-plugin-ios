package ios

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// xcodeSelectLink 是 xcode-select -s 写入的符号链接。
const xcodeSelectLink = "/var/db/xcode_select_link"

// xcodeEnv 描述当前用的 Xcode。
type xcodeEnv struct {
	DeveloperDir string   // 当前开发者目录（DEVELOPER_DIR 或 xcode-select 的设置）
	FromEnv      bool     // 来自 DEVELOPER_DIR 环境变量
	IsCLT        bool     // 指向 CommandLineTools
	Apps         []string // 已安装的 Xcode*.app
	Version      string   // 当前 Xcode 版本（CFBundleShortVersionString）
}

// AppPath 是当前 Xcode.app 路径（从开发者目录推出；不是 Xcode 时取第一个已安装的）。
func (x xcodeEnv) AppPath() string {
	if !x.IsCLT && strings.HasSuffix(x.DeveloperDir, "/Contents/Developer") {
		return strings.TrimSuffix(x.DeveloperDir, "/Contents/Developer")
	}
	if len(x.Apps) > 0 {
		return x.Apps[0]
	}
	return ""
}

// detectXcode 静态检查 Xcode：DEVELOPER_DIR 环境变量 > xcode-select 链接 > 默认位置。
func detectXcode(env map[string]string) xcodeEnv {
	x := xcodeEnv{Apps: findXcodeApps()}
	if d := env["DEVELOPER_DIR"]; d != "" {
		x.DeveloperDir, x.FromEnv = d, true
	} else if d := os.Getenv("DEVELOPER_DIR"); d != "" {
		x.DeveloperDir, x.FromEnv = d, true
	} else if d, err := os.Readlink(xcodeSelectLink); err == nil {
		x.DeveloperDir = d
	} else if sdk.DirExists("/Applications/Xcode.app/Contents/Developer") {
		x.DeveloperDir = "/Applications/Xcode.app/Contents/Developer"
	} else if sdk.DirExists("/Library/Developer/CommandLineTools") {
		x.DeveloperDir = "/Library/Developer/CommandLineTools"
	}
	x.DeveloperDir = strings.TrimSuffix(x.DeveloperDir, "/")
	if strings.HasSuffix(x.DeveloperDir, ".app") {
		x.DeveloperDir += "/Contents/Developer"
	}
	x.IsCLT = strings.Contains(x.DeveloperDir, "CommandLineTools")
	if app := x.AppPath(); app != "" {
		x.Version = xcodeVersion(app)
	}
	return x
}

// findXcodeApps 找 /Applications、~/Applications 下的 Xcode*.app（Xcode.app 排第一）。
func findXcodeApps() []string {
	var out []string
	for _, root := range []string{"/Applications", filepath.Join(sdk.HomeDir(), "Applications")} {
		m, _ := filepath.Glob(filepath.Join(root, "Xcode*.app"))
		for _, a := range m {
			if sdk.DirExists(filepath.Join(a, "Contents", "Developer")) {
				out = append(out, a)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return filepath.Base(out[i]) == "Xcode.app" && filepath.Base(out[j]) != "Xcode.app"
	})
	return out
}

// xcodeVersion 读 Xcode.app/Contents/version.plist。
func xcodeVersion(app string) string {
	data, err := os.ReadFile(filepath.Join(app, "Contents", "version.plist"))
	if err != nil {
		return ""
	}
	return parseXMLPlistStrings(string(data))["CFBundleShortVersionString"]
}

// supportsNewExportMethods：Xcode 15.3 起导出方式改名（app-store-connect / release-testing / debugging）。
func supportsNewExportMethods(version string) bool {
	return version == "" || compareVersions(version, "15.3") >= 0
}

// xcodeProblems 返回 Xcode 工具链问题（分析时显示在工程问题里）。licenseOK 为 nil 表示没检查。
func xcodeProblems(ctx *sdk.Context, x xcodeEnv, licenseOK *bool, firstLaunchOK *bool) []sdk.Diagnosis {
	var out []sdk.Diagnosis
	if len(x.Apps) == 0 && (x.IsCLT || x.DeveloperDir == "" || !sdk.DirExists(x.DeveloperDir)) {
		return []sdk.Diagnosis{{
			RuleID: "ios.no-xcode", Severity: "error",
			Title:   ctx.T("diag.noXcode.title"),
			Message: ctx.T("diag.noXcode.message"),
			Fixes:   []sdk.Fix{{ID: "appStore", Title: ctx.T("fix.installXcode"), Kind: sdk.FixOpenURL, Params: map[string]string{"url": "https://apps.apple.com/app/xcode/id497799835"}, Recommended: true}},
		}}
	}
	if x.IsCLT || (x.DeveloperDir != "" && !sdk.DirExists(x.DeveloperDir)) {
		app := x.Apps[0]
		dev := filepath.Join(app, "Contents", "Developer")
		out = append(out, sdk.Diagnosis{
			RuleID: "ios.xcode-select", Severity: "error",
			Title:   ctx.T("diag.xcodeSelect.title"),
			Message: ctx.T("diag.xcodeSelect.message", x.DeveloperDir, "sudo xcode-select -s "+dev),
			Fixes:   []sdk.Fix{{ID: "developerDir", Title: ctx.T("fix.useXcode", filepath.Base(app)), Kind: sdk.FixSetEnv, Params: map[string]string{"key": "DEVELOPER_DIR", "value": dev, "scope": "project"}, Recommended: true}},
		})
		return out
	}
	if licenseOK != nil && !*licenseOK {
		out = append(out, sdk.Diagnosis{
			RuleID: "ios.license", Severity: "error",
			Title:   ctx.T("diag.license.title"),
			Message: ctx.T("diag.license.message", "sudo xcodebuild -license accept"),
		})
	}
	if firstLaunchOK != nil && !*firstLaunchOK {
		out = append(out, sdk.Diagnosis{
			RuleID: "ios.first-launch", Severity: "warning",
			Title:   ctx.T("diag.firstLaunch.title"),
			Message: ctx.T("diag.firstLaunch.message", "sudo xcodebuild -runFirstLaunch"),
		})
	}
	return out
}

// checkLicense 跑 xcodebuild -license check / -checkFirstLaunchStatus（都很快，不需要权限）。
func checkLicense(ctx *sdk.Context, env map[string]string) (license, firstLaunch *bool) {
	if ctx == nil || ctx.Host == nil {
		return nil, nil
	}
	if r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcodebuild", Args: []string{"-license", "check"}, Env: env, Capture: true, TimeoutMs: 10000}); err == nil && !r.TimedOut && !r.Cancelled {
		ok := r.ExitCode == 0
		license = &ok
	}
	if r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcodebuild", Args: []string{"-checkFirstLaunchStatus"}, Env: env, Capture: true, TimeoutMs: 10000}); err == nil && !r.TimedOut && !r.Cancelled {
		ok := r.ExitCode == 0
		firstLaunch = &ok
	}
	return license, firstLaunch
}
