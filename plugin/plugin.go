// Package ios 是 Lemon Paperplane 的 iOS 插件：识别 Xcode 工作区/工程，静态解析 scheme 与目标设置，
// 用 xcodebuild 构建、devicectl / simctl 安装启动到真机或模拟器，CocoaPods / Swift 包依赖，签名与设备诊断，
// 以及「在 Xcode 中打开」「归档并上传 TestFlight」「导出 IPA」动作。
package ios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// PluginID 是插件 id，也是 facet id。
const PluginID = "ios"

// RunType 是运行配置类型。
const RunType = "ios-app"

// Plugin 实现 Detector、Analyzer、DependencyInstaller、RunPreparer、Diagnoser、FixApplier、ActionInvoker、ToolProvider。
type Plugin struct{}

// New 创建插件实例。
func New() any { return &Plugin{} }

// 识别时认领、不再往下识别的目录。
var claimDirs = []string{"Pods", "Carthage", "DerivedData", "build", "fastlane"}

// Detect：目录里有工作区或 .xcodeproj，且其中有 iOS 目标。只读文件，不跑外部命令。
func (p *Plugin) Detect(ctx *sdk.Context, d *sdk.DetectParams) (*sdk.DetectResult, error) {
	base := filepath.Base(d.Dir)
	if strings.HasSuffix(base, ".xcodeproj") || strings.HasSuffix(base, ".xcworkspace") {
		return &sdk.DetectResult{}, nil
	}
	var wss, projs []string
	for _, f := range d.Files {
		if !strings.HasSuffix(f, "/") {
			continue
		}
		name := strings.TrimSuffix(f, "/")
		switch {
		case strings.HasSuffix(name, ".xcworkspace") && name != "project.xcworkspace":
			wss = append(wss, name)
		case strings.HasSuffix(name, ".xcodeproj"):
			projs = append(projs, name)
		}
	}
	if len(wss) == 0 && len(projs) == 0 {
		return &sdk.DetectResult{}, nil
	}
	isIOS := map[string]bool{}
	checkProj := func(path string) bool {
		if v, ok := isIOS[path]; ok {
			return v
		}
		data, err := os.ReadFile(filepath.Join(path, "project.pbxproj"))
		v := err == nil && pbxprojIsIOS(string(data))
		isIOS[path] = v
		return v
	}
	var claims []string
	claimSub := func(projPath string) {
		if r, err := filepath.Rel(d.Dir, filepath.Dir(projPath)); err == nil && r != "." && !strings.HasPrefix(r, "..") {
			claims = append(claims, filepath.ToSlash(r))
		}
	}
	// 只留含 iOS 目标的容器，再按与分析相同的规则选一个（facet 数据里的容器与运行时一致）
	var iosContainers []container
	var paths []string
	for _, c := range findContainers(d.Dir) {
		ok := false
		for _, pp := range c.Projects {
			if c.Kind == "workspace" {
				claimSub(pp) // 工作区引用的其他目录里的工程（如 UnityExport/Unity-iPhone.xcodeproj）不单独成模块
			}
			if filepath.Base(pp) != "Pods.xcodeproj" && checkProj(pp) {
				ok = true
			}
		}
		if !ok {
			continue
		}
		iosContainers = append(iosContainers, c)
		paths = append(paths, c.Path)
		if c.Kind == "project" {
			// 主工程以子工程方式引用的工程也归这个模块
			if text, err := os.ReadFile(filepath.Join(c.Path, "project.pbxproj")); err == nil && strings.Contains(string(text), "projectReferences") {
				if proj, err := parseProject(c.Path, string(text)); err == nil {
					for _, sp := range proj.SubProjects {
						claimSub(sp)
					}
				}
			}
		}
	}
	if len(iosContainers) == 0 {
		return &sdk.DetectResult{}, nil
	}
	chosen, _ := chooseContainer(iosContainers, "", derivedDataFor(derivedDataRoot(), paths))
	name := strings.TrimSuffix(strings.TrimSuffix(chosen.Name(), ".xcworkspace"), ".xcodeproj")
	claims = append(claims, claimDirs...)
	claims = append(claims, wss...)
	claims = append(claims, projs...)
	return &sdk.DetectResult{
		Facets: []sdk.Facet{{ID: PluginID, Data: map[string]any{"container": chosen.Name(), "kind": chosen.Kind}}},
		Claims: uniq(claims),
		Name:   name,
	}, nil
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Analyze：静态解析容器、scheme、目标设置与依赖；顺带检查 Xcode，并取一次设备列表填下拉选项。
func (p *Plugin) Analyze(ctx *sdk.Context, in *sdk.AnalyzeParams) (*sdk.AnalyzeResult, error) {
	a := analyzeModule(in.Module.Dir, "")
	res := &sdk.AnalyzeResult{}
	res.Model, _ = json.Marshal(a.model)

	x := detectXcode(in.Env)
	var license, firstLaunch *bool
	if !x.IsCLT && x.DeveloperDir != "" {
		license, firstLaunch = checkLicense(ctx, xcodeExecEnv(in.Env))
	}
	res.Problems = append(res.Problems, xcodeProblems(ctx, x, license, firstLaunch)...)

	runnable := a.model.runnable()
	if x.Version != "" {
		res.Info = append(res.Info, sdk.InfoItem{Label: "Xcode", Value: x.Version})
	}
	if len(runnable) > 0 && runnable[0].DeploymentTarget != "" {
		res.Info = append(res.Info, sdk.InfoItem{Label: "iOS", Value: runnable[0].DeploymentTarget + "+", Icon: "icons/ios.svg"})
	}
	if a.model.Pods {
		res.Info = append(res.Info, sdk.InfoItem{Label: "CocoaPods", Value: ctx.T("deps.state." + podState(a.dir))})
	} else if a.model.Packages > 0 {
		res.Info = append(res.Info, sdk.InfoItem{Label: ctx.T("info.swiftPackages"), Value: itoa(a.model.Packages)})
	}

	st := depState(ctx, a)
	res.Dependencies = &st

	// 运行配置：每个可运行 scheme 一个
	schemeOpts := []sdk.Option{}
	for i, s := range runnable {
		schemeOpts = append(schemeOpts, sdk.Option{Value: s.Name, Label: s.Name})
		cfg := sdk.RunConfiguration{
			ID:       sdk.StableID(RunType, in.Module.RelDir, s.Name),
			Name:     s.Name,
			Type:     RunType,
			Options:  map[string]any{"scheme": s.Name, "configuration": s.LaunchConfig, "device": ""},
			Source:   "auto",
			Priority: 100 - i,
			Default:  i == 0,
		}
		res.RunConfigurations = append(res.RunConfigurations, cfg)
	}
	if len(a.containers) > 0 && len(runnable) == 0 {
		res.Problems = append(res.Problems, sdk.Diagnosis{RuleID: "ios.no-runnable-scheme", Severity: "info", Title: ctx.T("diag.noScheme.title"), Message: ctx.T("diag.noScheme.message", a.model.Container),
			Fixes: []sdk.Fix{openXcodeFix(ctx)}})
	}
	// 自动签名但没有团队：真机运行会失败（模拟器不需要签名）
	if len(runnable) > 0 && runnable[0].Team == "" && !strings.EqualFold(runnable[0].SignStyle, "Manual") {
		res.Problems = append(res.Problems, sdk.Diagnosis{RuleID: "ios.no-team-static", Severity: "warning", Title: ctx.T("diag.noTeamStatic.title", runnable[0].Target), Message: ctx.T("diag.noTeamStatic.message"),
			Fixes: []sdk.Fix{openXcodeFix(ctx)}})
	}

	cfgOpts := []sdk.Option{}
	for _, c := range a.model.Configs {
		cfgOpts = append(cfgOpts, sdk.Option{Value: c, Label: c})
	}
	containerOpts := []sdk.Option{{Value: "", Label: ctx.T("opt.container.auto", a.model.Container)}}
	for _, c := range a.model.Containers {
		containerOpts = append(containerOpts, sdk.Option{Value: c, Label: c})
	}
	deviceOpts := deviceOptions(ctx, x, in.Env)
	res.OptionValues = map[string][]sdk.Option{
		"ios.schemes":        schemeOpts,
		"ios.configurations": cfgOpts,
		"ios.devices":        deviceOpts,
		"ios.containers":     containerOpts,
	}
	return res, nil
}

// xcodeExecEnv：执行 xcrun / xcodebuild 时的环境（工程环境变量里可能有修复设置的 DEVELOPER_DIR）。
func xcodeExecEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	if v := env["DEVELOPER_DIR"]; v != "" {
		out["DEVELOPER_DIR"] = v
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// deviceOptions 是运行配置「设备」下拉框的选项：自动 + 可用的真机 + 模拟器。分析时取一次作初始值，
// 界面打开运行配置时经 options/list 实时再取（manifest 里 device 字段标了 live）。
func deviceOptions(ctx *sdk.Context, x xcodeEnv, env map[string]string) []sdk.Option {
	opts := []sdk.Option{{Value: "", Label: ctx.T("opt.device.auto")}}
	if x.IsCLT || len(x.Apps) == 0 {
		return opts
	}
	for _, d := range listDevices(ctx, xcodeExecEnv(env)) {
		if !d.Simulator && !d.Reachable() {
			continue
		}
		opts = append(opts, sdk.Option{Value: d.UDID, Label: d.Label(ctx.T("label.simulator"))})
	}
	return opts
}

// Options 实时提供下拉选项（options/list）：目前只有设备列表。
func (p *Plugin) Options(ctx *sdk.Context, in *sdk.OptionsParams) (*sdk.OptionsResult, error) {
	out := map[string][]sdk.Option{}
	for _, k := range in.Keys {
		if k == "ios.devices" {
			out[k] = deviceOptions(ctx, detectXcode(in.Env), in.Env)
		}
	}
	return &sdk.OptionsResult{Values: out}, nil
}
