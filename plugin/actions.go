package ios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// 动作 id。
const (
	ActionOpenXcode  = "openXcode"
	ActionTestFlight = "testflight"
	ActionExportIPA  = "exportIpa"
)

// InvokeAction：在 Xcode 中打开；归档并上传 TestFlight；导出 IPA。
func (p *Plugin) InvokeAction(ctx *sdk.Context, in *sdk.InvokeActionParams) (*sdk.InvokeActionResult, error) {
	switch in.Action {
	case ActionOpenXcode:
		if err := openInXcode(ctx, in.Module.Dir, in.Env); err != nil {
			return nil, err
		}
		return &sdk.InvokeActionResult{}, nil
	case ActionTestFlight:
		return p.archiveAndExport(ctx, in, exportOptions{Method: "app-store-connect", Destination: "upload"})
	case ActionExportIPA:
		method := "debugging"
		if m, _ := in.Args["method"].(string); m != "" {
			method = m
		}
		switch method {
		case "development":
			method = "debugging"
		case "ad-hoc":
			method = "release-testing"
		}
		if method != "debugging" && method != "release-testing" {
			return nil, sdk.UserError("%s", ctx.T("err.exportMethod", method))
		}
		return p.archiveAndExport(ctx, in, exportOptions{Method: method, Destination: "export"})
	}
	return nil, sdk.ErrNotSupported
}

// archiveAndExport：xcodebuild archive（默认 Release）到插件缓存目录，再 -exportArchive。
// 都带 -allowProvisioningUpdates，签名与上传用 Xcode 里已登录的账号。
func (p *Plugin) archiveAndExport(ctx *sdk.Context, in *sdk.InvokeActionParams, eo exportOptions) (*sdk.InvokeActionResult, error) {
	a := analyzeModule(in.Module.Dir, "")
	if a.chosen.Path == "" {
		return nil, sdk.UserError("%s", ctx.T("err.noContainer"))
	}
	x := detectXcode(in.Env)
	if x.IsCLT || x.AppPath() == "" {
		return nil, sdk.UserError("%s", ctx.T("err.noXcode"))
	}
	schemeName, _ := in.Args["scheme"].(string)
	if schemeName == "" {
		if rs := a.model.runnable(); len(rs) > 0 {
			schemeName = rs[0].Name
		}
	}
	sc := a.model.scheme(schemeName)
	if sc == nil {
		return nil, sdk.UserError("%s", ctx.T("err.noScheme", schemeName, a.model.Container))
	}
	config, _ := in.Args["configuration"].(string)
	if config == "" {
		config = sc.ArchiveConfig
	}
	if config == "" {
		config = "Release"
	}
	if !supportsNewExportMethods(x.Version) {
		eo.Method = legacyExportMethod(eo.Method)
	}
	eo.TeamID = sc.Team
	if strings.EqualFold(sc.SignStyle, "Automatic") {
		eo.SignStyle = "automatic"
	}

	stamp := time.Now().Format("20060102-150405")
	base := filepath.Join(cacheDir(ctx), "archives", sdk.StableID(in.Module.Dir), sc.Name+"-"+stamp)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	archive := filepath.Join(base, sc.Name+".xcarchive")
	env := in.Env
	allow := settingBool(ctx, SettingAllowProvisioning, true)

	// 1. 归档
	ctx.Host.Progress(in.TaskID, ctx.T("step.archive", sc.Name, config), -1)
	req := buildRequest{Container: a.chosen, Scheme: sc.Name, Configuration: config, Destination: "generic/platform=iOS", AllowProvisioning: allow, ArchivePath: archive}
	acmd, aargs, adisplay := filteredXcodebuild(ctx, false, req.archiveArgs())
	r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: acmd, Args: aargs, Display: adisplay, Cwd: a.dir, Env: env, Console: in.TaskID, Capture: true, Title: ctx.T("step.archive", sc.Name, config)})
	if err != nil {
		return nil, err
	}
	if r.Cancelled {
		return nil, sdk.NewError(sdk.ErrCodeCancelled, "cancelled")
	}
	if r.ExitCode != 0 {
		return nil, sdk.UserError("%s", failureText(ctx, "err.archive", r.ExitCode, r.Stdout+"\n"+r.Stderr))
	}

	// 2. 导出 / 上传
	optsPath := filepath.Join(base, "ExportOptions.plist")
	if err := os.WriteFile(optsPath, []byte(eo.plist()), 0o644); err != nil {
		return nil, err
	}
	exportDir := filepath.Join(base, "export")
	title := ctx.T("step.export", eo.Method)
	if eo.Destination == "upload" {
		title = ctx.T("step.upload")
	}
	ctx.Host.Progress(in.TaskID, title, -1)
	r, err = ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcodebuild", Args: exportArgs(archive, optsPath, exportDir, allow), Cwd: a.dir, Env: env, Console: in.TaskID, Capture: true, Title: title})
	if err != nil {
		return nil, err
	}
	if r.Cancelled {
		return nil, sdk.NewError(sdk.ErrCodeCancelled, "cancelled")
	}
	if r.ExitCode != 0 {
		key := "err.export"
		if eo.Destination == "upload" {
			key = "err.upload"
		}
		return nil, sdk.UserError("%s", failureText(ctx, key, r.ExitCode, r.Stdout+"\n"+r.Stderr))
	}
	if eo.Destination == "upload" {
		return &sdk.InvokeActionResult{Message: ctx.T("msg.uploaded", sc.Name, archive)}, nil
	}
	ipas, _ := filepath.Glob(filepath.Join(exportDir, "*.ipa"))
	if len(ipas) == 0 {
		return &sdk.InvokeActionResult{Message: ctx.T("msg.exportedDir", exportDir), OpenURL: "file://" + exportDir}, nil
	}
	return &sdk.InvokeActionResult{Message: ctx.T("msg.exported", ipas[0]), OpenURL: "file://" + exportDir}, nil
}

// failureText：失败说明 + 命中的诊断规则（动作任务没有诊断面板，直接把原因写进错误）。
func failureText(ctx *sdk.Context, key string, code int, output string) string {
	msg := ctx.T(key, code)
	if e := firstError(output); e != "" {
		msg += "\n" + e
	}
	if hint := explainOutput(ctx, output); hint != "" {
		msg += "\n" + hint
	}
	return msg
}

// explainOutput 用 manifest 里的诊断规则解释输出，返回第一条命中的「标题：说明」。
func explainOutput(ctx *sdk.Context, output string) string {
	if ctx == nil || ctx.Init == nil || ctx.Init.PluginDir == "" {
		return ""
	}
	m, err := sdk.LoadManifest(ctx.Init.PluginDir)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		if t := matchRules(m.Contributes.Diagnostics, line, func(s string) string { return resolveL10n(ctx, s) }); t != "" {
			return t
		}
	}
	return ""
}

// resolveL10n 解析 "%key%"。
func resolveL10n(ctx *sdk.Context, s string) string {
	if len(s) > 2 && strings.HasPrefix(s, "%") && strings.HasSuffix(s, "%") {
		return ctx.T(s[1 : len(s)-1])
	}
	return s
}

func cacheDir(ctx *sdk.Context) string {
	if ctx != nil && ctx.Init != nil && ctx.Init.CacheDir != "" {
		return ctx.Init.CacheDir
	}
	return filepath.Join(os.TempDir(), "paperplane-ios")
}

// ---------------------------------------------------------------------------
// 工具（API / MCP）

// DevicesResult 是 ios.devices 工具的返回。
type DevicesResult struct {
	Devices []Device `json:"devices"`
}

// CallTool：devices —— 列出能用来运行的真机与模拟器。
func (p *Plugin) CallTool(ctx *sdk.Context, in *sdk.ToolCallParams) (any, error) {
	switch in.Tool {
	case "devices":
		var args struct {
			All bool `json:"all"`
		}
		if len(in.Args) > 0 {
			_ = json.Unmarshal(in.Args, &args)
		}
		out := DevicesResult{Devices: []Device{}}
		for _, d := range listDevices(ctx, nil) {
			if args.All || d.Simulator || d.Reachable() {
				out.Devices = append(out.Devices, d)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown tool %s", in.Tool)
}
