package ios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

func loadManifest(t *testing.T) *sdk.Manifest {
	t.Helper()
	m, err := sdk.LoadManifest("..")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestDiagnosticRules 用真实（或按工具原文构造的）日志行检查每条规则：hint 必须在行里，正则可编译且命中，捕获组正确。
func TestDiagnosticRules(t *testing.T) {
	m := loadManifest(t)
	rules := map[string]sdk.DiagnosticRule{}
	for _, r := range m.Contributes.Diagnostics {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			t.Errorf("%s: %v", r.ID, err)
		}
		if r.Hint == "" {
			t.Errorf("%s: 缺少 hint", r.ID)
		}
		if _, dup := rules[r.ID]; dup {
			t.Errorf("%s 重复", r.ID)
		}
		rules[r.ID] = r
		for _, f := range r.Fixes {
			if f.Kind == sdk.FixCustom && f.ID != FixOpenXcode && f.ID != FixDownloadPlatform {
				t.Errorf("%s: 自定义修复 %s 没有实现", r.ID, f.ID)
			}
		}
	}
	cases := []struct{ rule, line, group1 string }{
		// 下面两行是本机 xcodebuild 实测输出
		{"ios.no-team", `/tmp/sample/HelloPP/HelloPP.xcodeproj: error: Signing for "HelloPP" requires a development team. Select a development team in the Signing & Capabilities editor. (in target 'HelloPP' from project 'HelloPP')`, "HelloPP"},
		{"ios.no-profiles", `/tmp/HelloPP.xcodeproj: error: No profiles for 'cn.lemonit.paperplane.sample.HelloPP' were found: Xcode couldn't find any iOS App Development provisioning profiles matching 'cn.lemonit.paperplane.sample.HelloPP'.`, "cn.lemonit.paperplane.sample.HelloPP"},
		{"ios.no-account", `error: No Account for Team "TEAM123456". Add a new account in Accounts settings or verify that your accounts have valid credentials. (in target 'App' from project 'App')`, "TEAM123456"},
		{"ios.no-certificate", `error: No signing certificate "iOS Development" found: No "iOS Development" signing certificate matching team ID "TEAM123456" with a private key was found.`, "iOS Development"},
		{"ios.device-not-registered", `error: Device "iPhone" isn't registered in your developer account. The device must be registered in order to be included in a provisioning profile.`, ""},
		{"ios.team-no-devices", `error: Your team has no devices from which to generate a provisioning profile. Connect a device to use or manually add device IDs in Certificates, Identifiers & Profiles.`, ""},
		{"ios.profile-missing-device", `error: Provisioning profile "iOS Team Provisioning Profile: com.example.app" doesn't include the currently selected device "iPhone" (identifier 00008120-000278280A3B401E).`, "iOS Team Provisioning Profile: com.example.app"},
		{"ios.install-no-profile", `ERROR: The application could not be verified. (0xe8008015 (A valid provisioning profile for this executable was not found.))`, ""},
		{"ios.untrusted-developer", `ERROR: The request was denied by service delegate (SBMainWorkspace) for reason: Security ("Unable to launch com.example.app because it has an invalid code signature, inadequate entitlements or its profile has not been explicitly trusted by the user").`, ""},
		// CoreDevice 框架里的原文
		{"ios.developer-mode", `ERROR: The operation failed since Developer Mode is turned off on the device. (com.apple.dt.CoreDeviceError error 1)`, ""},
		{"ios.developer-mode", `{ platform:iOS, id:00008120-000278280A3B401E, name:iPhone, error:Developer Mode disabled To use iPhone for development, enable Developer Mode in Settings → Privacy & Security. }`, ""},
		{"ios.device-locked", `ERROR: The operation failed since the device is locked. (com.apple.dt.CoreDeviceError error 4)`, ""},
		{"ios.device-locked", `{ platform:iOS, id:00008120-000278280A3B401E, name:测试的iPhone, error:测试的iPhone is locked. }`, ""},
		// 2026-11-18 真机实测：手机锁屏时 devicectl launch 的原文
		{"ios.device-locked", `                   The operation couldn’t be completed. Unable to launch com.example.app because the device was not, or could not be, unlocked. (FBSOpenApplicationErrorDomain error 7 (0x07))`, ""},
		{"ios.device-locked", `               NSLocalizedFailureReason = The request was denied by service delegate (SBMainWorkspace) for reason: Locked ("Unable to launch com.example.app because the device was not, or could not be, unlocked.").`, ""},
		{"ios.not-paired", `ERROR: The device is not paired with this host. (com.apple.dt.CoreDeviceError error 2)`, ""},
		{"ios.device-busy", `{ platform:iOS, id:00008120-000278280A3B401E, name:iPhone, error:iPhone is busy: Preparing iPhone for development. Xcode will continue when iPhone is finished. }`, "Preparing iPhone for development. Xcode will continue when iPhone is finished. "},
		{"ios.destination-not-found", `xcodebuild: error: Unable to find a destination matching the provided destination specifier:`, ""},
		{"ios.db-locked", `error: unable to attach DB: error: accessing build database "/Users/x/Library/Developer/Xcode/DerivedData/App-abc/Build/Intermediates.noindex/XCBuildData/build.db": database is locked Possibly there are two concurrent builds running in the same filesystem location.`, ""},
		{"ios.spm-resolve", `xcodebuild: error: Could not resolve package dependencies:`, ""},
		{"ios.spm-clone", `  Failed to clone repository https://github.com/google/GoogleSignIn-iOS.git:`, "https://github.com/google/GoogleSignIn-iOS.git:"},
		{"ios.spm-missing-product", `error: Missing package product 'GoogleSignIn' (in target 'App' from project 'App')`, "GoogleSignIn"},
		{"ios.license", `You have not agreed to the Xcode license agreements. You must agree to both license agreements below in order to use Xcode.`, ""},
		{"ios.xcode-select", `xcode-select: error: tool 'xcodebuild' requires Xcode, but active developer directory '/Library/Developer/CommandLineTools' is a command line tools instance`, "/Library/Developer/CommandLineTools"},
		{"ios.platform-missing", `error: iOS 18.2 is not installed. To use with Xcode, first download and install the platform`, "iOS 18.2"},
		{"ios.pods-out-of-sync", `error: The sandbox is not in sync with the Podfile.lock. Run 'pod install' or update your CocoaPods installation.`, ""},
		{"ios.pod-versions", `[!] CocoaPods could not find compatible versions for pod "Alamofire":`, "Alamofire"},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		r, ok := rules[c.rule]
		if !ok {
			t.Errorf("没有规则 %s", c.rule)
			continue
		}
		covered[c.rule] = true
		if !strings.Contains(c.line, r.Hint) {
			t.Errorf("%s: hint %q 不在行里", c.rule, r.Hint)
			continue
		}
		g := regexp.MustCompile(r.Pattern).FindStringSubmatch(c.line)
		if g == nil {
			t.Errorf("%s 没有命中：%s", c.rule, c.line)
			continue
		}
		if c.group1 != "" && (len(g) < 2 || g[1] != c.group1) {
			t.Errorf("%s: 捕获组 %q want %q", c.rule, g, c.group1)
		}
		// 每行只该命中一条规则（避免同一个错误报两遍）
		for id, other := range rules {
			if id == c.rule || !strings.Contains(c.line, other.Hint) {
				continue
			}
			if regexp.MustCompile(other.Pattern).MatchString(c.line) {
				t.Errorf("%s 的样例行也命中了 %s", c.rule, id)
			}
		}
	}
	for id := range rules {
		if !covered[id] {
			t.Errorf("规则 %s 没有样例行", id)
		}
	}
	// 不该误报
	negatives := map[string]string{
		"ios.device-locked": "error: database is locked",
		"ios.not-paired":    "iPad Air 13-inch (M3) is available (paired)",
		"ios.device-busy":   "Simulator is busy booting",
	}
	for id, line := range negatives {
		if regexp.MustCompile(rules[id].Pattern).MatchString(line) {
			t.Errorf("%s 误报：%s", id, line)
		}
	}
}

func TestReadyPatterns(t *testing.T) {
	m := loadManifest(t)
	lines := []string{"Launched application with com.example.app bundle identifier.", "Waiting for the application to terminate…"}
	for _, line := range lines {
		ok := false
		for _, r := range m.Contributes.ReadyPatterns {
			if strings.Contains(line, r.Hint) && regexp.MustCompile(r.Pattern).MatchString(line) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("就绪规则没有命中：%s", line)
		}
	}
}

func TestMatchRules(t *testing.T) {
	m := loadManifest(t)
	got := matchRules(m.Contributes.Diagnostics, `error: Signing for "App" requires a development team.`, func(s string) string {
		return map[string]string{"%diag.noTeam.title%": "「{1}」没有设置开发团队", "%diag.noTeam.message%": "选择团队"}[s]
	})
	if got != "「App」没有设置开发团队：选择团队" {
		t.Errorf("%q", got)
	}
}

// TestManifestShape：运行配置类型、动作、表单、设置与代码里的常量一致。
func TestManifestShape(t *testing.T) {
	m := loadManifest(t)
	if m.ID != PluginID || m.Executable != "bin/paperplane-plugin-ios" || m.Contributes.Facets[0].ID != PluginID {
		t.Fatal("id")
	}
	if !sdk.MatchFileName("App.xcworkspace/", m.Activation.Files) || !sdk.MatchFileName("App.xcodeproj/", m.Activation.Files) || sdk.MatchFileName("App.xcodeproj", m.Activation.Files) {
		t.Error("activation 只匹配目录")
	}
	rt := m.Contributes.RunConfigurationTypes[0]
	keys := map[string]sdk.FormField{}
	for _, o := range rt.Options {
		keys[o.Key] = o
	}
	for _, k := range []string{OptScheme, OptConfiguration, OptDevice, OptArgs, OptEnv, OptContainer} {
		if _, ok := keys[k]; !ok {
			t.Errorf("运行配置缺选项 %s", k)
		}
	}
	if rt.ID != RunType || keys[OptDevice].OptionsFrom != "ios.devices" || keys[OptScheme].OptionsFrom != "ios.schemes" || keys[OptConfiguration].Default != "Debug" {
		t.Errorf("run type %+v", rt)
	}
	acts := map[string]sdk.ActionDef{}
	for _, a := range m.Contributes.Actions {
		acts[a.ID] = a
	}
	if acts[ActionTestFlight].Confirm == "" {
		t.Error("TestFlight 上传必须有确认文案")
	}
	if len(acts[ActionExportIPA].Form) == 0 || acts[ActionExportIPA].Form[0].Key != "method" {
		t.Error("导出 IPA 要先选导出方式")
	}
	if _, ok := acts[ActionOpenXcode]; !ok {
		t.Error("openXcode")
	}
	if m.Contributes.Settings[0].Key != SettingAllowProvisioning {
		t.Error("setting")
	}
}

// TestL10nCoverage：manifest 里的 %key% 与代码里 ctx.T 的 key 在两份文案里都要有；两份文案 key 集合一致。
func TestL10nCoverage(t *testing.T) {
	keys := map[string]bool{}
	b, _ := os.ReadFile("../plugin.json")
	for _, m := range regexp.MustCompile(`%([\w.\-:]+)%`).FindAllStringSubmatch(string(b), -1) {
		keys[m[1]] = true
	}
	files, _ := filepath.Glob("*.go")
	tre := regexp.MustCompile(`ctx\.T\("([^"]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, m := range tre.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasSuffix(m[1], ".") {
				keys[m[1]] = true
			}
		}
	}
	for _, k := range []string{"err.archive", "err.export", "err.upload"} { // failureText 里按变量取
		keys[k] = true
	}
	bundles := map[string]map[string]string{}
	for _, loc := range []string{"zh-Hans", "en"} {
		var bundle map[string]string
		raw, err := os.ReadFile(filepath.Join("..", "l10n", loc+".json"))
		if err != nil || json.Unmarshal(raw, &bundle) != nil {
			t.Fatalf("l10n/%s.json: %v", loc, err)
		}
		bundles[loc] = bundle
		for k := range keys {
			if bundle[k] == "" {
				t.Errorf("%s 缺少 %s", loc, k)
			}
		}
		for _, st := range []string{DepsOK, DepsMissing, DepsOutdated, DepsNone, DepsUnknown} {
			if bundle["deps.state."+st] == "" {
				t.Errorf("%s 缺少 deps.state.%s", loc, st)
			}
		}
	}
	for k := range bundles["zh-Hans"] {
		if _, ok := bundles["en"][k]; !ok {
			t.Errorf("en 缺少 %s", k)
		}
	}
	for k := range bundles["en"] {
		if _, ok := bundles["zh-Hans"][k]; !ok {
			t.Errorf("zh-Hans 缺少 %s", k)
		}
	}
	// 占位符个数两份一致
	ph := regexp.MustCompile(`\{\d\}`)
	for k, zh := range bundles["zh-Hans"] {
		if a, b := len(ph.FindAllString(zh, -1)), len(ph.FindAllString(bundles["en"][k], -1)); a != b {
			t.Errorf("%s 占位符个数不一致：zh %d en %d", k, a, b)
		}
	}
}

// TestIcons：图标只用允许的 SVG 子集。
func TestIcons(t *testing.T) {
	data, err := os.ReadFile("../icons/ios.svg")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`<(\w+)`).FindAllStringSubmatch(string(data), -1) {
		switch m[1] {
		case "svg", "g", "path", "circle", "ellipse", "rect", "line", "polyline", "polygon":
		default:
			t.Errorf("不支持的元素 %s", m[1])
		}
	}
}
