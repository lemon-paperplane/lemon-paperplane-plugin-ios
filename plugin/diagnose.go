package ios

import (
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// 自定义修复 id。
const (
	FixOpenXcode        = "openXcode"
	FixDownloadPlatform = "downloadPlatform"
)

func openXcodeFix(ctx *sdk.Context) sdk.Fix {
	return sdk.Fix{ID: FixOpenXcode, Title: ctx.T("fix.openXcode"), Kind: sdk.FixCustom}
}

func retryFix(ctx *sdk.Context) sdk.Fix {
	return sdk.Fix{ID: "retry", Title: ctx.T("fix.retry"), Kind: sdk.FixRestart}
}

// Diagnose 补充声明式规则做不到的诊断：
//  1. 还没走到构建就失败（选设备失败）：列出设备，给每台一个「用这台设备运行」（setRunOption device）；
//  2. 构建失败但没有命中更具体的规则：汇总错误行。
func (p *Plugin) Diagnose(ctx *sdk.Context, in *sdk.DiagnoseParams) (*sdk.DiagnoseResult, error) {
	if in.Stage != "run" {
		return &sdk.DiagnoseResult{}, nil
	}
	var out []sdk.Diagnosis
	specific := false
	for _, d := range in.Matched {
		if strings.HasPrefix(d.RuleID, "ios.") {
			specific = true
		}
	}
	built := false
	failed := false
	for _, l := range in.LogTail {
		if strings.Contains(l, "$ xcodebuild") {
			built = true
		}
		if strings.Contains(l, "** BUILD FAILED **") {
			failed = true
		}
	}
	if !built && in.ExitCode == nil {
		if d := p.deviceDiagnosis(ctx, in); d != nil {
			out = append(out, *d)
		}
	}
	if failed && !specific {
		lines := errorLines(in.LogTail, 12)
		d := sdk.Diagnosis{RuleID: "ios.build-failed", Severity: "error", Title: ctx.T("diag.buildFailed.title"), Message: ctx.T("diag.buildFailed.message"),
			Fixes: []sdk.Fix{openXcodeFix(ctx), retryFix(ctx)}}
		if len(lines) > 0 {
			d.Detail = strings.Join(lines, "\n")
			d.Line = lines[0]
		}
		out = append(out, d)
	}
	return &sdk.DiagnoseResult{Diagnoses: out}, nil
}

// deviceDiagnosis 重新选一次设备，失败时给出原因与「用这台设备运行」修复。
func (p *Plugin) deviceDiagnosis(ctx *sdk.Context, in *sdk.DiagnoseParams) *sdk.Diagnosis {
	opts := map[string]any{}
	if in.Config != nil && in.Config.Options != nil {
		opts = in.Config.Options
	}
	a := analyzeModule(in.Module.Dir, sdk.OptString(opts, OptContainer, ""))
	minOS := ""
	if sc := a.model.scheme(sdk.OptString(opts, OptScheme, "")); sc != nil {
		minOS = sc.DeploymentTarget
	} else if rs := a.model.runnable(); len(rs) > 0 {
		minOS = rs[0].DeploymentTarget
	}
	devs := listDevices(ctx, nil)
	_, serr := selectDevice(devs, sdk.OptString(opts, OptDevice, ""), minOS)
	if serr == nil {
		return nil
	}
	return deviceDiagnosisFor(ctx, serr, devs, minOS)
}

// deviceDiagnosisFor 由选设备失败的原因生成诊断（可单测）。
func deviceDiagnosisFor(ctx *sdk.Context, serr *selectError, devs []Device, minOS string) *sdk.Diagnosis {
	name := ""
	if serr.Device != nil {
		name = serr.Device.Name
	}
	d := &sdk.Diagnosis{RuleID: "ios.device-" + serr.Kind, Severity: "error"}
	switch serr.Kind {
	case "multiple":
		d.Title, d.Message = ctx.T("diag.pickDevice.title"), ctx.T("diag.pickDevice.message", serr.Count)
	case "notFound":
		d.Title, d.Message = ctx.T("diag.deviceNotFound.title"), ctx.T("diag.deviceNotFound.message", serr.Want)
	case "unavailable":
		d.Title, d.Message = ctx.T("diag.deviceUnavailable.title", name), ctx.T("diag.deviceUnavailable.message")
	case "notPaired":
		d.Title, d.Message = ctx.T("diag.notPaired.title", name), ctx.T("diag.notPaired.message")
	case "devModeOff":
		d.Title, d.Message = ctx.T("diag.developerMode.title", name), ctx.T("diag.developerMode.message")
	case "osTooOld":
		d.Title, d.Message = ctx.T("diag.osTooOld.title", name, serr.Device.OS), ctx.T("diag.osTooOld.message", serr.Need)
	default:
		d.Title, d.Message = ctx.T("diag.noDevice.title"), ctx.T("diag.noDevice.message")
	}
	recommended := serr.Kind == "multiple"
	for _, c := range candidates(devs, 8) {
		if serr.Device != nil && c.UDID == serr.Device.UDID {
			continue
		}
		if minOS != "" && c.OS != "" && compareVersions(c.OS, minOS) < 0 {
			continue
		}
		d.Fixes = append(d.Fixes, sdk.Fix{
			ID:          "device-" + c.UDID,
			Title:       ctx.T("fix.useDevice", c.Label(ctx.T("label.simulator"))),
			Kind:        sdk.FixSetRunOption,
			Params:      map[string]string{"key": OptDevice, "value": c.UDID},
			Recommended: recommended,
		})
		recommended = false
	}
	if serr.Kind == "notFound" {
		// 指定的设备不在了：改回自动最省事
		d.Fixes = append(d.Fixes, sdk.Fix{ID: "device-auto", Title: ctx.T("fix.autoDevice"), Kind: sdk.FixSetRunOption, Params: map[string]string{"key": OptDevice, "value": ""}, Recommended: true})
	}
	d.Fixes = append(d.Fixes, retryFix(ctx))
	return d
}

// ApplyFix：在 Xcode 中打开；下载 iOS 平台（xcodebuild -downloadPlatform iOS）。
func (p *Plugin) ApplyFix(ctx *sdk.Context, in *sdk.ApplyFixParams) (*sdk.ApplyFixResult, error) {
	switch in.Fix.ID {
	case FixOpenXcode:
		if err := openInXcode(ctx, in.Module.Dir, in.Env); err != nil {
			return nil, err
		}
		return &sdk.ApplyFixResult{Message: ctx.T("msg.openedXcode")}, nil
	case FixDownloadPlatform:
		r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcodebuild", Args: []string{"-downloadPlatform", "iOS"}, Env: xcodeExecEnv(in.Env), Console: in.TaskID, Capture: in.TaskID == "", Title: "xcodebuild -downloadPlatform iOS"})
		if err != nil {
			return nil, err
		}
		if r.ExitCode != 0 {
			return nil, sdk.UserError("%s", ctx.T("err.step", "xcodebuild -downloadPlatform iOS", r.ExitCode, lastLine(r.Stderr+"\n"+r.Stdout)))
		}
		return &sdk.ApplyFixResult{Rerun: true, Message: ctx.T("msg.platformDownloaded")}, nil
	}
	return nil, sdk.ErrNotSupported
}

// openInXcode 用当前 Xcode 打开模块的工作区/工程。
func openInXcode(ctx *sdk.Context, dir string, env map[string]string) error {
	a := analyzeModule(dir, "")
	target := a.chosen.Path
	if target == "" {
		target = dir
	}
	x := detectXcode(env)
	args := []string{target}
	if app := x.AppPath(); app != "" {
		args = append([]string{"-a", app}, args...)
	} else {
		args = append([]string{"-a", "Xcode"}, args...)
	}
	r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "open", Args: args, Capture: true, TimeoutMs: 20000})
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return sdk.UserError("%s", ctx.T("err.openXcode", lastLine(r.Stderr)))
	}
	return nil
}

// matchRules 用声明式规则匹配一行（与内核做法一致：先 hint 字面量预筛，再正则；{1}… 替换捕获组），
// 返回「标题：说明」。resolve 负责把 %key% 翻成文案。
func matchRules(rules []sdk.DiagnosticRule, line string, resolve func(string) string) string {
	for _, r := range rules {
		if r.Hint != "" && !strings.Contains(line, r.Hint) {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		g := re.FindStringSubmatch(line)
		if g == nil {
			continue
		}
		title := fillGroups(resolve(r.Title), g)
		if r.Message == "" {
			return title
		}
		return title + "：" + fillGroups(resolve(r.Message), g)
	}
	return ""
}

func fillGroups(tpl string, g []string) string {
	for i := len(g) - 1; i >= 1; i-- {
		tpl = strings.ReplaceAll(tpl, "{"+strconv.Itoa(i)+"}", g[i])
	}
	return tpl
}
