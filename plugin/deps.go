package ios

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// 依赖状态常量（与 sdk.DependencyState.State 取值一致）。
const (
	DepsOK       = "ok"
	DepsMissing  = "missing"
	DepsOutdated = "outdated"
	DepsUnknown  = "unknown"
	DepsNone     = "none"
)

// podState 对比 Podfile、Podfile.lock、Pods/Manifest.lock（与 CocoaPods 构建阶段「Check Pods Manifest.lock」一致）。
func podState(dir string) string {
	if !sdk.FileExists(filepath.Join(dir, "Podfile")) {
		return DepsNone
	}
	lock, err := os.ReadFile(filepath.Join(dir, "Podfile.lock"))
	if err != nil {
		return DepsMissing
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "Pods", "Manifest.lock"))
	if err != nil {
		return DepsMissing
	}
	if !bytes.Equal(bytes.TrimSpace(lock), bytes.TrimSpace(manifest)) {
		return DepsOutdated
	}
	return DepsOK
}

// packageResolvedPath 返回容器的 Package.resolved（工作区放在 xcshareddata/swiftpm，工程放在 project.xcworkspace 里）。
func packageResolvedPath(c container) string {
	if c.Kind == "workspace" {
		return filepath.Join(c.Path, "xcshareddata", "swiftpm", "Package.resolved")
	}
	return filepath.Join(c.Path, "project.xcworkspace", "xcshareddata", "swiftpm", "Package.resolved")
}

// resolvedPins 数 Package.resolved 里锁定的包（v1 在 object.pins，v2/v3 在 pins）。文件不存在返回 -1。
func resolvedPins(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	var r struct {
		Pins   []json.RawMessage `json:"pins"`
		Object struct {
			Pins []json.RawMessage `json:"pins"`
		} `json:"object"`
	}
	if json.Unmarshal(data, &r) != nil {
		return 0
	}
	if len(r.Pins) > 0 {
		return len(r.Pins)
	}
	return len(r.Object.Pins)
}

// depState 汇总依赖状态：CocoaPods 需要显式安装，Swift 包由 xcodebuild 构建时自动解析。
func depState(ctx *sdk.Context, a *moduleAnalysis) sdk.DependencyState {
	if st := podState(a.dir); st != DepsNone {
		ds := sdk.DependencyState{State: st, Manager: "cocoapods"}
		switch st {
		case DepsMissing:
			ds.Message = ctx.T("deps.pods.missing")
		case DepsOutdated:
			ds.Message = ctx.T("deps.pods.outdated")
		}
		return ds
	}
	if a.model.Packages > 0 {
		n := resolvedPins(packageResolvedPath(a.chosen))
		if n < 0 {
			return sdk.DependencyState{State: DepsUnknown, Manager: "swiftpm", Message: ctx.T("deps.spm.unresolved")}
		}
		return sdk.DependencyState{State: DepsOK, Manager: "swiftpm", Message: ctx.T("deps.spm.pinned", n)}
	}
	return sdk.DependencyState{State: DepsNone}
}

// podCommand：Gemfile 里有 cocoapods 时用 bundle exec pod，否则 pod。
func podCommand(dir string) (string, []string) {
	if g, err := os.ReadFile(filepath.Join(dir, "Gemfile")); err == nil && strings.Contains(string(g), "cocoapods") {
		return "bundle", []string{"exec", "pod", "install"}
	}
	return "pod", []string{"install"}
}

// InstallDependencies：CocoaPods 工程跑 pod install（按 Podfile.lock 安装，只有 Podfile 改过时才会更新锁文件）；
// 只有 Swift 包时跑 xcodebuild -resolvePackageDependencies。
func (p *Plugin) InstallDependencies(ctx *sdk.Context, in *sdk.InstallDepsParams) (*sdk.InstallDepsResult, error) {
	a := analyzeModule(in.Module.Dir, "")
	if podState(a.dir) != DepsNone {
		if in.Clean {
			ctx.Host.Console(in.TaskID, ctx.T("msg.cleanPods"))
			_ = os.RemoveAll(filepath.Join(a.dir, "Pods"))
		}
		cmd, args := podCommand(a.dir)
		r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: cmd, Args: args, Cwd: a.dir, Env: sdk.MergeEnv(in.Env, map[string]string{"LANG": "en_US.UTF-8", "COCOAPODS_DISABLE_STATS": "true"}), Console: in.TaskID, Title: cmd + " " + strings.Join(args, " ")})
		if err != nil {
			return nil, err
		}
		if r.Cancelled {
			return nil, sdk.NewError(sdk.ErrCodeCancelled, "cancelled")
		}
		if r.ExitCode == 127 {
			return nil, sdk.UserError("%s", ctx.T("err.noPod"))
		}
		if r.ExitCode != 0 {
			return nil, sdk.UserError("%s", ctx.T("err.podInstall", r.ExitCode))
		}
		a = analyzeModule(in.Module.Dir, "")
		return &sdk.InstallDepsResult{Dependencies: depState(ctx, a)}, nil
	}
	if a.chosen.Path == "" {
		return &sdk.InstallDepsResult{Dependencies: sdk.DependencyState{State: DepsNone}}, nil
	}
	args := []string{a.chosen.xcodebuildFlag(), a.chosen.Path}
	if rs := a.model.runnable(); len(rs) > 0 {
		args = append(args, "-scheme", rs[0].Name)
	}
	args = append(args, "-resolvePackageDependencies")
	r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcodebuild", Args: args, Cwd: a.dir, Env: in.Env, Console: in.TaskID, Title: ctx.T("step.resolvePackages")})
	if err != nil {
		return nil, err
	}
	if r.Cancelled {
		return nil, sdk.NewError(sdk.ErrCodeCancelled, "cancelled")
	}
	if r.ExitCode != 0 {
		return nil, sdk.UserError("%s", ctx.T("err.resolvePackages", r.ExitCode))
	}
	return &sdk.InstallDepsResult{Dependencies: depState(ctx, a)}, nil
}
