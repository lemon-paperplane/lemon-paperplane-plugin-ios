package ios

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

func testCtx() *sdk.Context { return &sdk.Context{Context: context.Background()} }

func fixture(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseASCIIPlist(t *testing.T) {
	src := `// !$*UTF8*$!
{
	a = 1; /* 注释 */
	"quoted key" = "line\nnext \"q\" \U4e2d";
	list = (x, "y z", /* c */ w, );
	nested = { k = v; empty = ( ); };
	path = Foo/Bar.swift;
	data = <0a 0b>;
	dollar = "$(TARGET_NAME)";
	"KEY[sdk=iphoneos*]" = "-";
}`
	v, err := parseASCIIPlist(src)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["a"] != "1" || m["quoted key"] != "line\nnext \"q\" 中" || m["path"] != "Foo/Bar.swift" || m["data"] != "0a0b" || m["dollar"] != "$(TARGET_NAME)" {
		t.Errorf("%#v", m)
	}
	if !reflect.DeepEqual(m["list"], []any{"x", "y z", "w"}) {
		t.Errorf("list %#v", m["list"])
	}
	if n := m["nested"].(map[string]any); n["k"] != "v" || len(n["empty"].([]any)) != 0 {
		t.Errorf("nested %#v", n)
	}
	if _, err := parseASCIIPlist("{ a = (1, 2; }"); err == nil {
		t.Error("坏输入应报错")
	}
}

func TestParseProjectHello(t *testing.T) {
	p, err := loadProject(filepath.Join(fixture(t, "hello"), "HelloPP.xcodeproj"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || !reflect.DeepEqual(p.Configs, []string{"Debug", "Release"}) {
		t.Fatalf("targets %d configs %v", len(p.Targets), p.Configs)
	}
	tg := p.Targets[0]
	if tg.Name != "HelloPP" || !tg.IsApp() || tg.ProductFile != "HelloPP.app" {
		t.Errorf("target %+v", tg)
	}
	s := tg.Settings("Debug")
	// Bundle ID 来自 xcconfig 前缀 + $(PRODUCT_NAME:rfc1034identifier)，与 xcodebuild -showBuildSettings 实测一致
	if got := s.Get("PRODUCT_BUNDLE_IDENTIFIER"); got != "cn.lemonit.paperplane.sample.HelloPP" {
		t.Errorf("bundle id %q", got)
	}
	if s.Get("DEVELOPMENT_TEAM") != "ABCDE12345" || s.Get("IPHONEOS_DEPLOYMENT_TARGET") != "17.0" || s.Get("CODE_SIGN_STYLE") != "Automatic" || !s.IsIOS() {
		t.Errorf("settings team=%q dt=%q", s.Get("DEVELOPMENT_TEAM"), s.Get("IPHONEOS_DEPLOYMENT_TARGET"))
	}
	if got := s.Get("LD_RUNPATH_SEARCH_PATHS"); got != "@executable_path/Frameworks" {
		t.Errorf("inherited %q", got)
	}
	if got := s.Get("SWIFT_ACTIVE_COMPILATION_CONDITIONS"); got != "DEBUG" {
		t.Errorf("project-level inherited %q", got)
	}
}

func TestSettingsExpansion(t *testing.T) {
	r := &resolvedSettings{raw: map[string]string{
		"PRODUCT_NAME": "My App", "A": "$(B)-x", "B": "${C:lower}", "C": "ABC", "LOOP": "$(LOOP)",
		"ID": "com.example.$(PRODUCT_NAME:rfc1034identifier)", "MOD": "$(PRODUCT_NAME:c99extidentifier)",
	}, builtin: map[string]string{"TARGET_NAME": "T"}}
	cases := map[string]string{
		"A": "abc-x", "ID": "com.example.My-App", "MOD": "My_App", "LOOP": "$(LOOP)", "TARGET_NAME": "T",
	}
	for k, want := range cases {
		if got := r.Get(k); got != want {
			t.Errorf("%s = %q want %q", k, got, want)
		}
	}
	if got := r.expand("$(UNKNOWN:default=fallback)/$(UNSET)", 0); got != "fallback/$(UNSET)" {
		t.Errorf("default %q", got)
	}
}

func TestSchemeParse(t *testing.T) {
	dir := fixture(t, "hello")
	list := schemesIn(filepath.Join(dir, "HelloPP.xcodeproj"))
	if len(list) != 1 {
		t.Fatalf("schemes %d", len(list))
	}
	s := list[0]
	if s.Name != "HelloPP" || !s.Shared || s.LaunchConfig != "Debug" || s.ArchiveConfig != "Release" || s.Runnable == nil {
		t.Fatalf("%+v", s)
	}
	if s.Runnable.Project != filepath.Join(dir, "HelloPP.xcodeproj") || s.Runnable.BlueprintID != "AA0000000000000000000006" || s.Runnable.BuildableName != "HelloPP.app" {
		t.Errorf("runnable %+v", s.Runnable)
	}
	if !reflect.DeepEqual(s.Args, []string{"-verbose"}) || s.Env["PP_SAMPLE"] != "1" {
		t.Errorf("args %v env %v", s.Args, s.Env)
	}
}

func TestParseWorkspace(t *testing.T) {
	data := `<?xml version="1.0" encoding="UTF-8"?>
<Workspace version = "1.0">
   <FileRef location = "group:App.xcodeproj"></FileRef>
   <Group location = "group:Libs" name = "Libs">
      <FileRef location = "group:Net/Net.xcodeproj"></FileRef>
      <FileRef location = "group:README.md"></FileRef>
   </Group>
   <FileRef location = "container:Other/Other.xcodeproj"></FileRef>
   <FileRef location = "absolute:/abs/X.xcodeproj"></FileRef>
   <FileRef location = "group:App.xcodeproj"></FileRef>
</Workspace>`
	got := parseWorkspace(data, "/w", "/w/A.xcworkspace")
	want := []string{"/w/App.xcodeproj", "/w/Libs/Net/Net.xcodeproj", "/w/Other/Other.xcodeproj", "/abs/X.xcodeproj"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got := parseWorkspace(`<Workspace><FileRef location="self:"></FileRef></Workspace>`, "/w/P.xcodeproj", "/w/P.xcodeproj/project.xcworkspace"); !reflect.DeepEqual(got, []string{"/w/P.xcodeproj"}) {
		t.Errorf("self: %v", got)
	}
}

// UaaL 形态：工作区 = 主工程 + 被主工程当子工程引用的 Engine 工程（类似 Unity 导出的 Unity-iPhone）。
func TestAnalyzeWorkspaceWithSubproject(t *testing.T) {
	dir := fixture(t, "uaal")
	cs := findContainers(dir)
	if len(cs) != 2 || cs[0].Kind != "workspace" || cs[1].Kind != "project" {
		t.Fatalf("containers %+v", cs)
	}
	if len(cs[0].Projects) != 2 || cs[0].Projects[1] != filepath.Join(dir, "Engine", "Engine.xcodeproj") {
		t.Fatalf("workspace projects %v", cs[0].Projects)
	}
	c, why := chooseContainer(cs, "", nil)
	if c.Kind != "workspace" || why != "workspace" {
		t.Fatalf("chosen %+v %s", c, why)
	}
	m := analyzeContainer(dir, c, projectCache{})
	var names []string
	for _, s := range m.Schemes {
		names = append(names, s.Name+":"+s.Skip)
	}
	if strings.Join(names, ",") != "App:,AppClip:,Engine-iPhone:subproject" {
		t.Fatalf("schemes %v", names)
	}
	app := m.scheme("App")
	// [config=Debug] 条件设置覆盖无条件的；目标级部署目标覆盖工程级
	if app.BundleID != "com.example.app.debug" || app.DeploymentTarget != "16.6" || app.Team != "TEAM123456" || app.SignStyle != "Automatic" || app.ArchiveConfig != "Release" || app.Project != "App.xcodeproj" {
		t.Errorf("app %+v", app)
	}
	clip := m.scheme("AppClip")
	if clip.BundleID != "com.example.app.Clip" || clip.ProductType != "com.apple.product-type.application.on-demand-install-capable" || clip.DeploymentTarget != "15.0" {
		t.Errorf("clip %+v", clip)
	}
	if !reflect.DeepEqual(m.Configs, []string{"Debug", "Release"}) {
		t.Errorf("configs %v（子工程的 ReleaseForRunning 不该出现）", m.Configs)
	}
	if len(m.runnable()) != 2 {
		t.Errorf("runnable %d", len(m.runnable()))
	}
	// 只用主工程：看不到子工程的 scheme
	m2 := analyzeContainer(dir, cs[1], projectCache{})
	if len(m2.Schemes) != 2 {
		t.Errorf("project schemes %+v", m2.Schemes)
	}
	// 只剩子工程的应用时不跳过
	m3 := analyzeContainer(dir, container{Path: filepath.Join(dir, "Engine", "Engine.xcodeproj"), Kind: "project", Projects: []string{filepath.Join(dir, "Engine", "Engine.xcodeproj")}}, projectCache{})
	if len(m3.runnable()) != 1 || m3.runnable()[0].LaunchConfig != "ReleaseForRunning" {
		t.Errorf("engine alone %+v", m3.Schemes)
	}
}

func TestChooseContainerFollowsXcode(t *testing.T) {
	dir := fixture(t, "uaal")
	cs := findContainers(dir)
	dd := t.TempDir()
	write := func(name, ws string, at time.Time) {
		d := filepath.Join(dd, name)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>LastAccessedDate</key>
	<date>`+at.UTC().Format(time.RFC3339)+`</date>
	<key>WorkspacePath</key>
	<string>`+ws+`</string>
</dict>
</plist>`), 0o644)
	}
	now := time.Now()
	write("App-aaaa", filepath.Join(dir, "App.xcworkspace"), now.Add(-48*time.Hour))
	write("App-bbbb", filepath.Join(dir, "App.xcodeproj"), now.Add(-time.Hour))
	write("App-cccc", "/elsewhere/App.xcodeproj", now)
	entries := derivedDataFor(dd, []string{cs[0].Path, cs[1].Path})
	if len(entries) != 2 || entries[cs[1].Path].Dir != filepath.Join(dd, "App-bbbb") {
		t.Fatalf("entries %+v", entries)
	}
	c, why := chooseContainer(cs, "", entries)
	if c.Path != cs[1].Path || why != "xcode" {
		t.Errorf("应跟随 Xcode 最近打开的工程：%s %s", c.Name(), why)
	}
	c, why = chooseContainer(cs, "App.xcworkspace", entries)
	if c.Kind != "workspace" || why != "option" {
		t.Errorf("运行配置指定优先：%s %s", c.Name(), why)
	}
}

func TestPodsProject(t *testing.T) {
	dir := fixture(t, "pods")
	a := analyzeModule(dir, "")
	if a.chosen.Kind != "workspace" || a.model.ContainerWhy != "pods" {
		t.Fatalf("CocoaPods 必须用工作区：%+v %s", a.chosen, a.model.ContainerWhy)
	}
	rs := a.model.runnable()
	if len(rs) != 1 || rs[0].Name != "Demo" || !rs[0].Auto {
		t.Fatalf("没有 scheme 文件时按应用目标自动生成：%+v", a.model.Schemes)
	}
	if rs[0].BundleID != "org.example.pods.demo" {
		t.Errorf("xcconfig #include? 与行尾注释：%q", rs[0].BundleID)
	}
	proj := a.projects.get(filepath.Join(dir, "Demo.xcodeproj"))
	if got := proj.Targets[0].Settings("Debug").Get("OTHER_LDFLAGS"); got != `-l"c++" -framework "Alamofire" -ObjC` {
		t.Errorf("OTHER_LDFLAGS %q", got)
	}
	if podState(dir) != DepsOK || !a.model.Pods {
		t.Errorf("pods %s", podState(dir))
	}
	st := depState(testCtx(), a)
	if st.State != DepsOK || st.Manager != "cocoapods" {
		t.Errorf("dep %+v", st)
	}

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "Podfile"), []byte("pod 'A'"), 0o644)
	if podState(tmp) != DepsMissing {
		t.Error("没有 Podfile.lock 应为 missing")
	}
	_ = os.WriteFile(filepath.Join(tmp, "Podfile.lock"), []byte("PODS:\n  - A (1.0)\n"), 0o644)
	if podState(tmp) != DepsMissing {
		t.Error("没有 Pods/Manifest.lock 应为 missing")
	}
	_ = os.MkdirAll(filepath.Join(tmp, "Pods"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, "Pods", "Manifest.lock"), []byte("PODS:\n  - A (0.9)\n"), 0o644)
	if podState(tmp) != DepsOutdated {
		t.Error("两个锁文件不一致应为 outdated")
	}
	if podState(fixture(t, "hello")) != DepsNone {
		t.Error("没有 Podfile")
	}
	if cmd, args := podCommand(dir); cmd != "pod" || strings.Join(args, " ") != "install" {
		t.Errorf("pod command %s %v", cmd, args)
	}
	_ = os.WriteFile(filepath.Join(tmp, "Gemfile"), []byte("gem 'cocoapods', '1.15.2'"), 0o644)
	if cmd, args := podCommand(tmp); cmd != "bundle" || strings.Join(args, " ") != "exec pod install" {
		t.Errorf("bundler %s %v", cmd, args)
	}
}

func TestSwiftPackages(t *testing.T) {
	dir := fixture(t, "uaal")
	a := analyzeModule(dir, "App.xcworkspace")
	if a.model.Packages != 1 {
		t.Errorf("packages %d", a.model.Packages)
	}
	if n := resolvedPins(packageResolvedPath(a.chosen)); n != 2 {
		t.Errorf("pins %d", n)
	}
	st := depState(testCtx(), a)
	if st.State != DepsOK || st.Manager != "swiftpm" {
		t.Errorf("%+v", st)
	}
	b := analyzeModule(dir, "App.xcodeproj")
	if st := depState(testCtx(), b); st.State != DepsUnknown {
		t.Errorf("工程容器没有 Package.resolved：%+v", st)
	}
	if got := packageResolvedPath(container{Path: "/x/A.xcodeproj", Kind: "project"}); got != "/x/A.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved" {
		t.Error(got)
	}
}

func TestDetect(t *testing.T) {
	pl := &Plugin{}
	files := func(dir string) []string {
		es, _ := os.ReadDir(dir)
		var out []string
		for _, e := range es {
			if e.IsDir() {
				out = append(out, e.Name()+"/")
			} else {
				out = append(out, e.Name())
			}
		}
		return out
	}
	hello := fixture(t, "hello")
	r, _ := pl.Detect(testCtx(), &sdk.DetectParams{Dir: hello, Files: files(hello)})
	if len(r.Facets) != 1 || r.Facets[0].ID != "ios" || r.Name != "HelloPP" || r.Facets[0].Data["kind"] != "project" {
		t.Fatalf("hello %+v", r)
	}
	uaal := fixture(t, "uaal")
	r, _ = pl.Detect(testCtx(), &sdk.DetectParams{Dir: uaal, Files: files(uaal)})
	if len(r.Facets) != 1 || r.Facets[0].Data["container"] != "App.xcworkspace" || r.Name != "App" {
		t.Fatalf("uaal %+v", r)
	}
	claims := strings.Join(r.Claims, ",")
	for _, c := range []string{"Engine", "Pods", "App.xcworkspace", "App.xcodeproj"} {
		if !strings.Contains(","+claims+",", ","+c+",") {
			t.Errorf("claims 缺 %s：%s", c, claims)
		}
	}
	// .xcodeproj 内部（project.xcworkspace）不识别
	r, _ = pl.Detect(testCtx(), &sdk.DetectParams{Dir: filepath.Join(hello, "HelloPP.xcodeproj"), Files: []string{"project.pbxproj", "project.xcworkspace/"}})
	if len(r.Facets) != 0 {
		t.Error(".xcodeproj 内部不该识别")
	}
	// 纯 macOS 工程不是 iOS
	mac := t.TempDir()
	_ = os.MkdirAll(filepath.Join(mac, "Mac.xcodeproj"), 0o755)
	_ = os.WriteFile(filepath.Join(mac, "Mac.xcodeproj", "project.pbxproj"), []byte("{ objects = { X = { buildSettings = { SDKROOT = macosx; SUPPORTED_PLATFORMS = macosx; }; }; }; }"), 0o644)
	r, _ = pl.Detect(testCtx(), &sdk.DetectParams{Dir: mac, Files: []string{"Mac.xcodeproj/"}})
	if len(r.Facets) != 0 {
		t.Error("macOS 工程不是 iOS")
	}
	// 多平台目标（SDKROOT = auto + SUPPORTED_PLATFORMS 含 iphoneos）
	if !pbxprojIsIOS(`SDKROOT = auto; SUPPORTED_PLATFORMS = "iphoneos iphonesimulator macosx";`) || pbxprojIsIOS(`SUPPORTED_PLATFORMS = "macosx xros";`) {
		t.Error("pbxprojIsIOS")
	}
}

func TestAnalyzeResult(t *testing.T) {
	pl := &Plugin{}
	dir := fixture(t, "uaal")
	res, err := pl.Analyze(testCtx(), &sdk.AnalyzeParams{Module: sdk.ModuleInfo{Dir: dir, RelDir: "ios"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RunConfigurations) != 2 {
		t.Fatalf("run configs %+v", res.RunConfigurations)
	}
	c0 := res.RunConfigurations[0]
	if c0.Name != "App" || c0.Type != RunType || !c0.Default || c0.Options["scheme"] != "App" || c0.Options["configuration"] != "Debug" || c0.Options["device"] != "" {
		t.Errorf("config %+v", c0)
	}
	again, _ := pl.Analyze(testCtx(), &sdk.AnalyzeParams{Module: sdk.ModuleInfo{Dir: dir, RelDir: "ios"}})
	if again.RunConfigurations[0].ID != c0.ID || again.RunConfigurations[1].ID == c0.ID {
		t.Error("运行配置 id 必须稳定且互不相同")
	}
	for _, k := range []string{"ios.schemes", "ios.configurations", "ios.devices", "ios.containers"} {
		if res.OptionValues[k] == nil {
			t.Errorf("OptionValues[%s] 不能是 null", k)
		}
	}
	if len(res.OptionValues["ios.schemes"]) != 2 || res.OptionValues["ios.devices"][0].Value != "" {
		t.Errorf("options %+v", res.OptionValues)
	}
	if res.Dependencies == nil || res.Dependencies.Manager != "swiftpm" {
		t.Errorf("deps %+v", res.Dependencies)
	}
}
