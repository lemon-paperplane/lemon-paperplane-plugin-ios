package ios

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// 运行配置选项。
const (
	OptScheme        = "scheme"
	OptConfiguration = "configuration"
	OptDevice        = "device"
	OptShowWarnings  = "showWarnings"
	OptArgs          = "args"
	OptEnv           = "env"
	OptContainer     = "container"
)

// SettingAllowProvisioning：构建真机/归档时是否带 -allowProvisioningUpdates（用 Xcode 已登录的账号创建证书与描述文件、注册设备）。
const SettingAllowProvisioning = "allowProvisioningUpdates"

// launchEnv 是 Xcode 运行应用时也会设置的环境：stdout 不缓冲，os_log / NSLog 同时写到 stderr，日志才能实时进控制台。
var launchEnv = map[string]string{"NSUnbufferedIO": "YES", "OS_ACTIVITY_DT_MODE": "enable"}

// PrepareRun：选设备 → xcodebuild 构建（输出进运行控制台）→ 找产物 → 读 Bundle ID → 安装 → 返回启动命令。
// 启动命令由内核托管：真机 devicectl launch --console，模拟器 simctl launch --console-pty，应用退出即结束，停止时发 SIGTERM（转发给应用）。
func (p *Plugin) PrepareRun(ctx *sdk.Context, in *sdk.PrepareRunParams) (*sdk.LaunchSpec, error) {
	opts := in.Config.Options
	a := analyzeModule(in.Module.Dir, sdk.OptString(opts, OptContainer, ""))
	if a.chosen.Path == "" {
		return nil, sdk.UserError("%s", ctx.T("err.noContainer"))
	}
	x := detectXcode(in.Env)
	if x.IsCLT || x.AppPath() == "" {
		return nil, sdk.UserError("%s", ctx.T("err.noXcode"))
	}
	env := in.Env // 已含工程环境变量（修复设置的 DEVELOPER_DIR 也在里面）

	schemeName := sdk.OptString(opts, OptScheme, "")
	if schemeName == "" {
		if rs := a.model.runnable(); len(rs) > 0 {
			schemeName = rs[0].Name
		}
	}
	sc := a.model.scheme(schemeName)
	if sc == nil {
		return nil, sdk.UserError("%s", ctx.T("err.noScheme", schemeName, a.model.Container))
	}
	config := sdk.OptString(opts, OptConfiguration, sc.LaunchConfig)

	// 1. 设备
	ctx.Host.Console(in.RunID, ctx.T("step.devices"))
	devs := listDevices(ctx, xcodeExecEnv(in.Env))
	dev, serr := selectDevice(devs, sdk.OptString(opts, OptDevice, ""), sc.DeploymentTarget)
	if serr != nil {
		return nil, sdk.UserError("%s", selectErrorText(ctx, serr))
	}
	ctx.Host.Console(in.RunID, ctx.T("step.device", dev.Label(ctx.T("label.simulator"))))

	// 2. 构建
	req := buildRequest{Container: a.chosen, Scheme: sc.Name, Configuration: config, Destination: destinationFor(*dev)}
	if !dev.Simulator && settingBool(ctx, SettingAllowProvisioning, true) {
		req.AllowProvisioning, req.RegisterDevice = true, true
	}
	bcmd, bargs, bdisplay := filteredXcodebuild(ctx, sdk.OptBool(opts, OptShowWarnings, false), req.buildArgs())
	r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: bcmd, Args: bargs, Display: bdisplay, Cwd: a.dir, Env: env, Console: in.RunID, Capture: true,
		Title: ctx.T("step.build", sc.Name, config, a.model.Container)})
	if err != nil {
		return nil, err
	}
	if r.Cancelled {
		return nil, sdk.NewError(sdk.ErrCodeCancelled, "cancelled")
	}
	if r.ExitCode != 0 {
		if e := firstError(r.Stdout, r.Stderr); e != "" {
			return nil, sdk.UserError("%s", ctx.T("err.buildWith", r.ExitCode, e))
		}
		return nil, sdk.UserError("%s", ctx.T("err.build", r.ExitCode))
	}

	// 3. 产物
	ctx.Host.Console(in.RunID, ctx.T("step.locate"))
	r, err = ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcodebuild", Args: req.showSettingsArgs(), Cwd: a.dir, Env: env, Capture: true, TimeoutMs: 120000})
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		return nil, sdk.UserError("%s", ctx.T("err.settings", r.ExitCode, shorten(strings.TrimSpace(r.Stderr), 600)))
	}
	app, _, perr := appFromBuildSettings([]byte(r.Stdout), sc.Target, sc.Product)
	if perr != nil {
		return nil, sdk.UserError("%s", ctx.T("err.noProduct", perr.Error()))
	}
	r, err = ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "plutil", Args: []string{"-extract", "CFBundleIdentifier", "raw", app + "/Info.plist"}, Capture: true, TimeoutMs: 10000})
	if err != nil {
		return nil, err
	}
	bundleID := strings.TrimSpace(r.Stdout)
	if r.ExitCode != 0 || bundleID == "" {
		return nil, sdk.UserError("%s", ctx.T("err.bundleId", app))
	}
	ctx.Host.Console(in.RunID, ctx.T("step.product", app, bundleID))

	// 4. 安装
	if dev.Simulator {
		if !dev.Booted {
			if err := runStep(ctx, in.RunID, env, ctx.T("step.boot", dev.Name), "xcrun", "simctl", "bootstatus", dev.UDID, "-b"); err != nil {
				return nil, err
			}
			// 让模拟器窗口出现（simctl 启动的是无界面的设备）
			_, _ = ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "open", Args: []string{"-a", "Simulator", "--args", "-CurrentDeviceUDID", dev.UDID}, Env: env, Capture: true, TimeoutMs: 15000})
		}
		if err := runStep(ctx, in.RunID, env, ctx.T("step.install", dev.Name), "xcrun", simInstallArgs(dev.UDID, app)...); err != nil {
			return nil, err
		}
	} else {
		if err := runStep(ctx, in.RunID, env, ctx.T("step.install", dev.Name), "xcrun", deviceInstallArgs(dev.UDID, app)...); err != nil {
			return nil, err
		}
	}

	// 5. 启动（内核托管）
	args := append([]string{}, sc.Args...)
	args = append(args, sdk.SplitArgs(sdk.OptString(opts, OptArgs, ""))...)
	appEnv := sdk.MergeEnv(launchEnv, sc.Env, optEnv(opts))
	spec := launchSpec(*dev, bundleID, args, appEnv)
	spec.Cwd = a.dir
	ctx.Host.Console(in.RunID, ctx.T("step.launch", bundleID, dev.Name))
	return spec, nil
}

// settingBool 读布尔型插件设置。
func settingBool(ctx *sdk.Context, key string, def bool) bool {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Setting(key, def).(bool); ok {
		return v
	}
	return def
}

// runStep 执行一个前置步骤（输出进运行控制台），失败时带上错误行。
func runStep(ctx *sdk.Context, runID string, env map[string]string, title, cmd string, args ...string) error {
	r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: cmd, Args: args, Env: env, Console: runID, Capture: true, Title: title})
	if err != nil {
		return err
	}
	if r.Cancelled {
		return sdk.NewError(sdk.ErrCodeCancelled, "cancelled")
	}
	if r.ExitCode != 0 {
		detail := firstError(r.Stderr, r.Stdout)
		if detail == "" {
			detail = lastLine(r.Stderr + "\n" + r.Stdout)
		}
		return sdk.UserError("%s", ctx.T("err.step", title, r.ExitCode, detail))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return shorten(l, 400)
		}
	}
	return ""
}

func deviceInstallArgs(udid, app string) []string {
	return []string{"devicectl", "device", "install", "app", "--device", udid, app}
}

func simInstallArgs(udid, app string) []string {
	return []string{"simctl", "install", udid, app}
}

// launchSpec 组装启动命令。应用的环境变量经 DEVICECTL_CHILD_ / SIMCTL_CHILD_ 前缀传给应用。
func launchSpec(d Device, bundleID string, appArgs []string, appEnv map[string]string) *sdk.LaunchSpec {
	spec := &sdk.LaunchSpec{Command: "xcrun", StopSignal: "SIGTERM", StopTimeoutMs: 10000, Env: map[string]string{}}
	prefix := "DEVICECTL_CHILD_"
	if d.Simulator {
		prefix = "SIMCTL_CHILD_"
		spec.Args = []string{"simctl", "launch", "--console-pty", "--terminate-running-process", d.UDID, bundleID}
		spec.Args = append(spec.Args, appArgs...)
		// simctl 启动成功先打印「<bundleId>: <pid>」
		spec.ReadyPatterns = []string{"^" + regexp.QuoteMeta(bundleID) + `: \d+`}
		spec.Display = "simctl launch " + bundleID + " → " + d.Name
	} else {
		spec.Args = []string{"devicectl", "device", "process", "launch", "--console", "--terminate-existing", "--device", d.UDID, bundleID}
		if len(appArgs) > 0 {
			spec.Args = append(spec.Args, "--")
			spec.Args = append(spec.Args, appArgs...)
		}
		spec.ReadyPatterns = []string{`Launched application with`, `Waiting for the application to terminate`}
		spec.Display = "devicectl launch " + bundleID + " → " + d.Name
	}
	keys := make([]string, 0, len(appEnv))
	for k := range appEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		spec.Env[prefix+k] = appEnv[k]
	}
	if len(appArgs) > 0 {
		spec.Display += " " + strings.Join(appArgs, " ")
	}
	return spec
}

// optEnv 读运行配置选项里的 env（keyvalue）。
func optEnv(opts map[string]any) map[string]string {
	out := map[string]string{}
	switch t := opts[OptEnv].(type) {
	case map[string]any:
		for k, v := range t {
			if s, ok := v.(string); ok {
				out[k] = s
			} else {
				b, _ := json.Marshal(v)
				out[k] = string(b)
			}
		}
	case map[string]string:
		for k, v := range t {
			out[k] = v
		}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				if k, v, ok := strings.Cut(s, "="); ok {
					out[k] = v
				}
			}
		}
	}
	return out
}

// selectErrorText 把选设备失败翻成给人看的说明。
func selectErrorText(ctx *sdk.Context, e *selectError) string {
	name := ""
	if e.Device != nil {
		name = e.Device.Name
	}
	switch e.Kind {
	case "multiple":
		return ctx.T("err.multipleDevices", e.Count)
	case "notFound":
		return ctx.T("err.deviceNotFound", e.Want)
	case "unavailable":
		return ctx.T("err.deviceUnavailable", name)
	case "notPaired":
		return ctx.T("err.deviceNotPaired", name)
	case "devModeOff":
		return ctx.T("err.developerMode", name)
	case "osTooOld":
		return ctx.T("err.osTooOld", name, e.Device.OS, e.Need)
	}
	return ctx.T("err.noDevice")
}
