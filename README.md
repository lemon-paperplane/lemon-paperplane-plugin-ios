# lemon-paperplane-plugin-ios

Lemon Paperplane 的 iOS 插件（id `ios`）。只在 macOS 上有用，需要完整的 Xcode。

## 识别

- 目录里有 `*.xcworkspace`（不含 `.xcodeproj` 里的 `project.xcworkspace`）或 `*.xcodeproj`，且其中有 iOS 目标（pbxproj 里 `SDKROOT = iphoneos`，或 `SUPPORTED_PLATFORMS` 含 `iphoneos`）时，给 facet `ios`。
- 认领 `Pods`、`Carthage`、`DerivedData`、`build`、`fastlane`、工作区/工程包本身，以及工作区或主工程以子工程方式引用的工程所在目录（如 Unity 导出的 `UnityExport/`），不再单独成模块。

## 分析（静态，不调 xcodebuild）

- 自带 OpenStep plist 解析器读 `project.pbxproj`：目标、产物类型、构建配置、`xcconfig`（含 `#include` / `#include?`）、`$(inherited)`、`$(VAR:rfc1034identifier)` 等修饰符、`KEY[sdk=iphoneos*]` / `KEY[config=Debug]` 条件设置、`projectReferences`、Swift 包引用。
- scheme：共享的（`xcshareddata/xcschemes`）+ 当前用户的（`xcuserdata/<用户>.xcuserdatad/xcschemes`），工作区自己的 + 各工程的；顺序取 `xcschememanagement.plist` 的 `orderHint`。工程里一个 scheme 文件都没有时，按应用目标自动生成（与 Xcode「自动创建 scheme」一致）。
- 可运行的 scheme：`LaunchAction` 里有 `BuildableProductRunnable`、产物是 `.app`、目标是应用（含 App Clip，排除手表应用）且面向 iOS。被主工程当子工程引用的工程里的应用（如 Unity 的 `Unity-iPhone`）在还有别的可运行 scheme 时跳过。
- 每个可运行 scheme 一个 `ios-app` 运行配置，选项：`scheme`、`configuration`（默认取 scheme 的 Launch 配置，一般是 Debug）、`device`（空表示自动）、`args`、`env`、`container`（高级）。
- 选容器（`-workspace` / `-project`）：运行配置指定 > CocoaPods 工作区 > Xcode 最近用来打开这个工程的容器（读 `~/Library/Developer/Xcode/DerivedData/*/info.plist` 的 `WorkspacePath`，与 Xcode 共用 DerivedData 增量构建）> 有工作区优先工作区。
- 依赖：CocoaPods 对比 `Podfile.lock` 与 `Pods/Manifest.lock`（ok / missing / outdated），安装用 `pod install`（Gemfile 里有 cocoapods 时 `bundle exec pod install`）；只有 Swift 包时数 `Package.resolved` 的锁定条目，安装是 `xcodebuild -resolvePackageDependencies`，平时由 xcodebuild 构建时自动解析。
- Xcode 检查：`xcode-select` 指向 CommandLineTools（修复：只给本工程设 `DEVELOPER_DIR`，或给出 `sudo xcode-select -s …` 命令）、没装 Xcode（App Store 链接）、没同意许可（`xcodebuild -license check`，给出 `sudo xcodebuild -license accept`）、首次启动组件（`xcodebuild -checkFirstLaunchStatus`）。
- 下拉选项：`ios.schemes`、`ios.configurations`、`ios.containers`、`ios.devices`（`devicectl list devices --json-output` 里能连上的真机 + `simctl list devices available --json` 的 iOS 模拟器，已启动的在前；取不到不报错）。

## 运行

1. 选设备：指定了就用它（检查能否连上、是否已信任、开发者模式、系统版本不低于部署目标）；自动时恰好一台可用真机就用它，否则用已启动的模拟器，否则报错，诊断里给每台设备一个「用 X 运行」（`setRunOption device`）。
2. 构建：`xcodebuild -workspace|-project … -scheme … -configuration … -destination id=<udid> [-allowProvisioningUpdates -allowProvisioningDeviceRegistration] -quiet build`，输出进运行控制台；不带 `-derivedDataPath`，与 Xcode 共用默认 DerivedData。
3. 找产物：同样参数 `-showBuildSettings -json`，取目标的 `TARGET_BUILD_DIR` + `FULL_PRODUCT_NAME`；Bundle ID 用 `plutil -extract CFBundleIdentifier raw`。
4. 安装：真机 `xcrun devicectl device install app`；模拟器先 `simctl bootstatus -b`（并打开 Simulator），再 `simctl install`。
5. 启动（内核托管）：真机 `xcrun devicectl device process launch --console --terminate-existing --device <udid> <bundleId>`，模拟器 `xcrun simctl launch --console-pty --terminate-running-process <udid> <bundleId>`；应用退出即结束，停止发 SIGTERM（devicectl / simctl 转发给应用）。scheme 里启用的启动参数与环境变量照样传给应用，另加 Xcode 也会设的 `NSUnbufferedIO=YES`、`OS_ACTIVITY_DT_MODE=enable`。

设置 `allowProvisioningUpdates`（默认开）关掉后，真机构建、归档、导出都不带 `-allowProvisioningUpdates`。

## 诊断

声明式规则：缺开发团队、找不到描述文件、Xcode 没登录团队账号、缺签名证书、设备未注册、描述文件不含设备、未信任开发者、开发者模式关闭、设备锁屏、未信任电脑、设备准备中、xcodebuild 找不到设备、构建数据库被占用（Xcode 正在构建同一工程）、Swift 包解析/下载失败、没同意许可、xcode-select 指向命令行工具、缺 iOS 平台（可直接下载）、Pods 不同步、CocoaPods 版本冲突。`run/diagnose` 补充：选设备失败时的「用这台设备运行」、没命中具体规则的构建失败（汇总错误行）。修复：在 Xcode 中打开（custom）、重试（restart）。

## 动作

- 在 Xcode 中打开（工程工具栏）。
- 归档并上传 TestFlight：默认 scheme 用它的 Archive 配置（一般是 Release）`xcodebuild archive` 到插件缓存目录 → `-exportArchive`（`method=app-store-connect`、`destination=upload`、`teamID`），带 `-allowProvisioningUpdates`，用 Xcode 已登录的账号上传；有确认文案。
- 导出 IPA：表单选开发 / Ad Hoc（Xcode 15.3 起的 `debugging` / `release-testing`，老 Xcode 自动换回 `development` / `ad-hoc`），导出到插件缓存目录并在访达中打开。
- 通过 API / MCP（`project.action`）调用时，`args` 里还可以传 `scheme`、`configuration`。界面上的动作表单目前拿不到 `optionsFrom` 动态选项，所以表单里没有放这两项。
- 动作任务没有诊断面板，归档/导出失败时插件用同一套声明式规则解释输出，把原因写进错误信息。

## 工具

`ios.devices`（MCP `ios_devices`）：列出能用来运行的真机与模拟器。

构建：`./build.sh`；测试：`go test ./...`。
