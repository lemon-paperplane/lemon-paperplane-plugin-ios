package ios

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// devicectl.json 是本机 Xcode 27.2 devicectl list devices --json-output 的真实输出（已脱敏）：
// 一台无线可用的 iPad、一台不可用的 iPad、一台无线可用的 iPhone。
func TestParseDevicectl(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "devicectl.json"))
	if err != nil {
		t.Fatal(err)
	}
	devs, err := parseDevicectl(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 {
		t.Fatalf("devices %+v", devs)
	}
	ipad, air, iphone := devs[0], devs[1], devs[2]
	if ipad.UDID != "00008001-0000000000000001" || ipad.Model != "iPad Pro (11-inch)" || ipad.OS != "26.6" || !ipad.Paired || !ipad.Reachable() || ipad.Transport != "localNetwork" || ipad.DeveloperMode != "enabled" {
		t.Errorf("ipad %+v", ipad)
	}
	if air.Reachable() || air.Name != "测试的iPad" {
		t.Errorf("不可用的 iPad %+v", air)
	}
	if iphone.Name != "测试的iPhone" || iphone.Model != "iPhone 14 Pro" || iphone.OS != "27.2.1" || !iphone.Reachable() {
		t.Errorf("iphone %+v", iphone)
	}
	if got := iphone.Label("模拟器"); got != "测试的iPhone · iPhone 14 Pro · iOS 27.2.1" {
		t.Errorf("label %q", got)
	}
	// 只有旧字段（Xcode 15/16）
	old := `{"result":{"devices":[{"identifier":"X","connectionProperties":{"pairingState":"paired","tunnelState":"connected","transportType":"wired"},"deviceProperties":{"name":"Old","osVersionNumber":"17.5","developerModeStatus":"disabled"},"hardwareProperties":{"udid":"U1","platform":"iOS","marketingName":"iPhone 12","reality":"physical"}},
	{"identifier":"W","hardwareProperties":{"udid":"U2","platform":"watchOS","reality":"physical"}}]}}`
	devs, err = parseDevicectl([]byte(old))
	if err != nil || len(devs) != 1 || devs[0].UDID != "U1" || devs[0].DeveloperMode != "disabled" || !devs[0].Reachable() || devs[0].Transport != "wired" {
		t.Errorf("old %+v %v", devs, err)
	}
}

func TestParseSimctl(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "simctl.json"))
	devs, err := parseSimctl(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range devs {
		names = append(names, d.Name+"@"+d.OS)
	}
	// 已启动的在前；只要 iOS；不可用的去掉；同状态按系统版本新的在前
	if strings.Join(names, ",") != "iPhone 15@17.5,iPad Air 11-inch (M2)@18.2,iPhone 16@18.2" {
		t.Errorf("%v", names)
	}
	if !devs[0].Booted || !devs[0].Simulator || !devs[0].Reachable() {
		t.Errorf("%+v", devs[0])
	}
	// 本机没有装模拟器运行时时的真实输出
	devs, err = parseSimctl([]byte("{\n  \"devices\" : {\n\n  }\n}\n"))
	if err != nil || len(devs) != 0 {
		t.Errorf("empty %v %v", devs, err)
	}
}

func TestSelectDevice(t *testing.T) {
	phone := Device{UDID: "P1", Name: "Phone", OS: "27.2", State: "disconnected", Transport: "localNetwork", Paired: true, DeveloperMode: "enabled"}
	pad := Device{UDID: "P2", Name: "Pad", OS: "26.6", State: "connected", Transport: "wired", Paired: true, DeveloperMode: "enabled"}
	gone := Device{UDID: "P3", Name: "Gone", OS: "27.2", State: "unavailable", Paired: true}
	unpaired := Device{UDID: "P4", Name: "New", OS: "27.0", State: "connected", Transport: "wired"}
	devoff := Device{UDID: "P5", Name: "Off", OS: "27.0", State: "connected", Transport: "wired", Paired: true, DeveloperMode: "disabled"}
	old := Device{UDID: "P6", Name: "Old", OS: "15.8", State: "connected", Transport: "wired", Paired: true}
	simBooted := Device{UDID: "S1", Name: "iPhone 16", OS: "18.2", Simulator: true, Booted: true}
	simOff := Device{UDID: "S2", Name: "iPad", OS: "18.2", Simulator: true}

	cases := []struct {
		name  string
		devs  []Device
		want  string
		minOS string
		pick  string
		err   string
	}{
		{"恰好一台真机", []Device{phone, gone, simBooted}, "", "", "P1", ""},
		{"多台真机但有已启动模拟器", []Device{phone, pad, simBooted}, "", "", "S1", ""},
		{"多台真机", []Device{phone, pad, simOff}, "", "", "", "multiple"},
		{"没有真机用已启动模拟器", []Device{gone, simOff, simBooted}, "", "", "S1", ""},
		{"什么都没有", []Device{gone, simOff}, "", "", "", "noDevice"},
		{"指定 udid", []Device{phone, pad}, "P2", "", "P2", ""},
		{"指定名字", []Device{phone, pad}, "Pad", "", "P2", ""},
		{"指定的不在", []Device{phone}, "ZZ", "", "", "notFound"},
		{"指定的连不上", []Device{gone}, "P3", "", "", "unavailable"},
		{"没信任", []Device{unpaired}, "", "", "", "notPaired"},
		{"开发者模式关", []Device{devoff}, "", "", "", "devModeOff"},
		{"系统太低", []Device{old}, "", "16.6", "", "osTooOld"},
		{"指定模拟器（未启动也可以）", []Device{phone, simOff}, "S2", "", "S2", ""},
	}
	for _, c := range cases {
		d, err := selectDevice(c.devs, c.want, c.minOS)
		switch {
		case c.err != "":
			if err == nil || err.Kind != c.err {
				t.Errorf("%s: want %s got %v %v", c.name, c.err, d, err)
			}
		case err != nil || d.UDID != c.pick:
			t.Errorf("%s: want %s got %v %v", c.name, c.pick, d, err)
		}
	}
	if _, err := selectDevice([]Device{phone, pad}, "", ""); err.Count != 2 {
		t.Errorf("count %d", err.Count)
	}
}

func TestXcodebuildArgs(t *testing.T) {
	ws := container{Path: "/p/App.xcworkspace", Kind: "workspace"}
	dev := Device{UDID: "00008120-000278280A3B401E"}
	r := buildRequest{Container: ws, Scheme: "App", Configuration: "Debug", Destination: destinationFor(dev), AllowProvisioning: true, RegisterDevice: true}
	if got := strings.Join(r.buildArgs(), " "); got != "-workspace /p/App.xcworkspace -scheme App -configuration Debug -destination id=00008120-000278280A3B401E -allowProvisioningUpdates -allowProvisioningDeviceRegistration -quiet build" {
		t.Errorf("build %s", got)
	}
	if got := strings.Join(r.showSettingsArgs(), " "); got != "-workspace /p/App.xcworkspace -scheme App -configuration Debug -destination id=00008120-000278280A3B401E -showBuildSettings -json" {
		t.Errorf("settings %s", got)
	}
	for _, a := range r.buildArgs() {
		if a == "-derivedDataPath" {
			t.Error("不能带 -derivedDataPath（要与 Xcode 共享 DerivedData）")
		}
	}
	sim := buildRequest{Container: container{Path: "/p/App.xcodeproj", Kind: "project"}, Scheme: "App", Configuration: "Debug", Destination: destinationFor(Device{UDID: "S1", Simulator: true})}
	if got := strings.Join(sim.buildArgs(), " "); got != "-project /p/App.xcodeproj -scheme App -configuration Debug -destination id=S1 -quiet build" {
		t.Errorf("sim %s", got)
	}
	ar := buildRequest{Container: ws, Scheme: "App", Configuration: "Release", Destination: "generic/platform=iOS", AllowProvisioning: true, ArchivePath: "/c/App.xcarchive"}
	if got := strings.Join(ar.archiveArgs(), " "); got != "-workspace /p/App.xcworkspace -scheme App -configuration Release -destination generic/platform=iOS -archivePath /c/App.xcarchive -allowProvisioningUpdates -quiet archive" {
		t.Errorf("archive %s", got)
	}
	if got := strings.Join(exportArgs("/c/App.xcarchive", "/c/E.plist", "/c/out", true), " "); got != "-exportArchive -archivePath /c/App.xcarchive -exportOptionsPlist /c/E.plist -exportPath /c/out -allowProvisioningUpdates" {
		t.Errorf("export %s", got)
	}
	if got := strings.Join(exportArgs("/a", "/b", "/c", false), " "); strings.Contains(got, "allowProvisioning") {
		t.Errorf("关掉设置时不带 -allowProvisioningUpdates：%s", got)
	}
}

func TestAppFromBuildSettings(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "showbuildsettings.json"))
	app, bs, err := appFromBuildSettings(append([]byte("Command line invocation: noise\n"), data...), "HelloPP", "HelloPP.app")
	if err != nil || app != "/tmp/sample/dd/Build/Products/Debug-iphoneos/HelloPP.app" || bs["PRODUCT_BUNDLE_IDENTIFIER"] != "cn.lemonit.paperplane.sample.HelloPP" {
		t.Errorf("%s %v", app, err)
	}
	// 目标名对不上时按产物名 / 第一个 .app
	if app, _, _ := appFromBuildSettings(data, "Other", "HelloPP.app"); !strings.HasSuffix(app, "/HelloPP.app") {
		t.Error(app)
	}
	if app, _, _ := appFromBuildSettings(data, "", ""); !strings.HasSuffix(app, "/HelloPP.app") {
		t.Error("应跳过 framework 取 .app：" + app)
	}
	if _, _, err := appFromBuildSettings([]byte("garbage"), "", ""); err == nil {
		t.Error("坏输出应报错")
	}
}

func TestFirstError(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "build-failed.txt"))
	if got := firstError(string(data)); got != "/tmp/sample/HelloBad/HelloPP/HelloPPApp.swift:10:19: error: cannot convert value of type 'String' to specified type 'Int'" {
		t.Errorf("first %q", got)
	}
	sign, _ := os.ReadFile(filepath.Join("testdata", "signing-errors.txt"))
	if got := firstError(string(sign)); !strings.Contains(got, `Signing for "HelloPP" requires a development team`) {
		t.Errorf("signing %q", got)
	}
	lines := errorLines(strings.Split(string(data), "\n"), 5)
	if len(lines) != 1 {
		t.Errorf("errorLines 去掉无用的 generic 行、过长的命令行：%v", lines)
	}
	if firstError("all good\nwarning: x") != "" {
		t.Error("no error")
	}
}

func TestLaunchSpec(t *testing.T) {
	dev := Device{UDID: "00008120-000278280A3B401E", Name: "iPhone"}
	s := launchSpec(dev, "com.example.app", []string{"-verbose"}, map[string]string{"A": "1", "NSUnbufferedIO": "YES"})
	if s.Command != "xcrun" || strings.Join(s.Args, " ") != "devicectl device process launch --console --terminate-existing --device 00008120-000278280A3B401E com.example.app -- -verbose" {
		t.Errorf("device %v", s.Args)
	}
	if s.StopSignal != "SIGTERM" || s.Env["DEVICECTL_CHILD_A"] != "1" || s.Env["DEVICECTL_CHILD_NSUnbufferedIO"] != "YES" || len(s.Env) != 2 {
		t.Errorf("device env %+v %s", s.Env, s.StopSignal)
	}
	if !matchAnyPattern(s.ReadyPatterns, "Launched application with com.example.app bundle identifier.") {
		t.Error("device ready")
	}
	sim := launchSpec(Device{UDID: "S1", Name: "iPhone 16", Simulator: true}, "com.example.app", nil, map[string]string{"A": "1"})
	if strings.Join(sim.Args, " ") != "simctl launch --console-pty --terminate-running-process S1 com.example.app" || sim.Env["SIMCTL_CHILD_A"] != "1" {
		t.Errorf("sim %v %v", sim.Args, sim.Env)
	}
	if !matchAnyPattern(sim.ReadyPatterns, "com.example.app: 4242") || matchAnyPattern(sim.ReadyPatterns, "com.example.appx: 1") {
		t.Error("sim ready")
	}
	// 选项里的 env
	if e := optEnv(map[string]any{"env": map[string]any{"K": "V", "N": 1.0}}); e["K"] != "V" || e["N"] != "1" {
		t.Errorf("optEnv %v", e)
	}
	if e := optEnv(map[string]any{"env": []any{"A=B=C"}}); e["A"] != "B=C" {
		t.Errorf("optEnv list %v", e)
	}
}

func matchAnyPattern(pats []string, line string) bool {
	for _, p := range pats {
		if regexp.MustCompile(p).MatchString(line) {
			return true
		}
	}
	return false
}

func TestExportOptions(t *testing.T) {
	o := exportOptions{Method: "app-store-connect", Destination: "upload", TeamID: "TEAM<1>", SignStyle: "automatic"}
	pl := o.plist()
	kv := parseXMLPlistStrings(pl)
	if kv["method"] != "app-store-connect" || kv["destination"] != "upload" || kv["teamID"] != "TEAM<1>" || kv["signingStyle"] != "automatic" || kv["uploadSymbols"] != "true" {
		t.Errorf("%v\n%s", kv, pl)
	}
	if legacyExportMethod("release-testing") != "ad-hoc" || legacyExportMethod("debugging") != "development" {
		t.Error("legacy")
	}
	if !supportsNewExportMethods("27.2") || !supportsNewExportMethods("15.3") || supportsNewExportMethods("15.2") {
		t.Error("version gate")
	}
}

func TestDeviceDiagnosis(t *testing.T) {
	phone := Device{UDID: "P1", Name: "Phone", OS: "27.2", State: "disconnected", Transport: "localNetwork", Paired: true}
	pad := Device{UDID: "P2", Name: "Pad", OS: "15.0", State: "connected", Transport: "wired", Paired: true}
	sim := Device{UDID: "S1", Name: "iPhone 16", OS: "18.2", Simulator: true}
	devs := []Device{phone, pad, sim}
	_, serr := selectDevice(devs, "", "")
	d := deviceDiagnosisFor(testCtx(), serr, devs, "")
	if d.RuleID != "ios.device-multiple" || len(d.Fixes) != 4 {
		t.Fatalf("%+v", d)
	}
	f := d.Fixes[0]
	if f.Kind != sdk.FixSetRunOption || f.Params["key"] != "device" || f.Params["value"] != "P1" || !f.Recommended || d.Fixes[1].Recommended {
		t.Errorf("fix %+v", f)
	}
	if d.Fixes[3].Kind != sdk.FixRestart {
		t.Error("最后是重试")
	}
	// 部署目标 16 时系统太旧的 Pad 不给
	d = deviceDiagnosisFor(testCtx(), serr, devs, "16.0")
	for _, f := range d.Fixes {
		if f.Params["value"] == "P2" {
			t.Error("系统太旧的设备不该出现在修复里")
		}
	}
	// 指定的设备不在：给其他设备 + 改回自动
	_, serr = selectDevice(devs, "GONE", "")
	d = deviceDiagnosisFor(testCtx(), serr, devs, "")
	if d.RuleID != "ios.device-notFound" || d.Fixes[len(d.Fixes)-2].ID != "device-auto" || d.Fixes[len(d.Fixes)-2].Params["value"] != "" {
		t.Errorf("notFound %+v", d.Fixes)
	}
}

func TestDiagnoseBuildFailed(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "build-failed.txt"))
	tail := append([]string{"构建 HelloPP（Debug）", "$ xcodebuild -project HelloPP.xcodeproj -scheme HelloPP build"}, strings.Split(string(data), "\n")...)
	res, _ := (&Plugin{}).Diagnose(testCtx(), &sdk.DiagnoseParams{Stage: "run", LogTail: tail})
	if len(res.Diagnoses) != 1 || res.Diagnoses[0].RuleID != "ios.build-failed" || !strings.Contains(res.Diagnoses[0].Detail, "HelloPPApp.swift:10:19") {
		t.Fatalf("%+v", res.Diagnoses)
	}
	if res.Diagnoses[0].Fixes[0].ID != FixOpenXcode || res.Diagnoses[0].Fixes[0].Kind != sdk.FixCustom {
		t.Error("第一个修复是在 Xcode 中打开")
	}
	// 已命中更具体的规则时不再报笼统的构建失败
	res, _ = (&Plugin{}).Diagnose(testCtx(), &sdk.DiagnoseParams{Stage: "run", LogTail: tail, Matched: []sdk.Diagnosis{{RuleID: "ios.no-team"}}})
	if len(res.Diagnoses) != 0 {
		t.Errorf("%+v", res.Diagnoses)
	}
	res, _ = (&Plugin{}).Diagnose(testCtx(), &sdk.DiagnoseParams{Stage: "install", LogTail: tail})
	if len(res.Diagnoses) != 0 {
		t.Error("只诊断运行")
	}
}

func TestXcodeProblems(t *testing.T) {
	ctx := testCtx()
	ps := xcodeProblems(ctx, xcodeEnv{DeveloperDir: "/Library/Developer/CommandLineTools", IsCLT: true, Apps: []string{"/Applications/Xcode.app"}}, nil, nil)
	if len(ps) != 1 || ps[0].RuleID != "ios.xcode-select" || ps[0].Fixes[0].Kind != sdk.FixSetEnv || ps[0].Fixes[0].Params["value"] != "/Applications/Xcode.app/Contents/Developer" {
		t.Errorf("clt %+v", ps)
	}
	ps = xcodeProblems(ctx, xcodeEnv{DeveloperDir: "/Library/Developer/CommandLineTools", IsCLT: true}, nil, nil)
	if len(ps) != 1 || ps[0].RuleID != "ios.no-xcode" || ps[0].Fixes[0].Kind != sdk.FixOpenURL {
		t.Errorf("no xcode %+v", ps)
	}
	no := false
	ps = xcodeProblems(ctx, xcodeEnv{DeveloperDir: "/", Apps: []string{"/Applications/Xcode.app"}}, &no, &no)
	if len(ps) != 2 || ps[0].RuleID != "ios.license" || ps[1].RuleID != "ios.first-launch" {
		t.Errorf("license %+v", ps)
	}
	x := xcodeEnv{DeveloperDir: "/Applications/Xcode-beta.app/Contents/Developer", Apps: []string{"/Applications/Xcode.app"}}
	if x.AppPath() != "/Applications/Xcode-beta.app" {
		t.Error(x.AppPath())
	}
	if e := detectXcode(map[string]string{"DEVELOPER_DIR": "/Applications/Xcode.app"}); e.DeveloperDir != "/Applications/Xcode.app/Contents/Developer" || !e.FromEnv {
		t.Errorf("DEVELOPER_DIR 指向 .app 时补全：%+v", e)
	}
}
