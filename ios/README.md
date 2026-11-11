# ARTEX iOS

面向 iPhone 的 ARTEX 原生客户端，使用 SwiftUI，最低 iOS 26，无第三方 App 依赖。支持通过 HTTPS 连接多个 ARTEX 实例，包括 Tailscale 私网入口。

## 打开和运行

使用 Xcode 26.3 或更新版本打开 `ARTEX.xcodeproj`，选择 ARTEX Scheme。真机运行时，在 Signing & Capabilities 中选择自己的开发团队，并按需修改 Bundle ID。工程不包含个人签名配置。

首次使用时，从左上角进入“管理远程源”，添加服务器的 HTTPS 地址并登录。访问 Tailscale 地址需要 iPhone 已加入相同 Tailnet。已有安装会继续读取本机保存的源和 Keychain 凭据。

工程文件由 `python3 scripts/generate_project.py` 生成；新增 Swift 文件后重新运行生成器。

```sh
cd ios
swift test --scratch-path /tmp/artex-ios-swift-build
xcodebuild -project ARTEX.xcodeproj -scheme ARTEX \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath /tmp/artex-ios-derived CODE_SIGNING_ALLOWED=NO build
```

UI 测试使用模拟器和 `--demo` 演示数据，不向真实服务器创建任务或发送消息。先用 `xcodebuild -showdestinations -project ARTEX.xcodeproj -scheme ARTEX` 查看可用模拟器，再将其 ID 传给 `-destination 'platform=iOS Simulator,id=<ID>'` 运行 `test`。

## 已实现

- 多远程源的切换、添加、编辑、排序、连接检查和登录。
- Tasks 搜索、筛选、创建、暂停/继续、重命名、置顶与复制配置。
- 任务内 Sessions 列表、分页和详情；主对话发送消息/Hint、查看处理状态、停止回复。
- Findings 分页、筛选、详情及 Markdown 报告。
- 按源隔离的 Keychain 凭据、本机缓存和草稿；超时的写操作不会自动重发。

任务删除仅在服务器明确声明支持保留证据时启用。

## 认证

支持 `mobile_tokens_v1` 的服务端在账号登录后自动签发设备 Token。最长 90 天、闲置 30 天失效，密码修改使现有 Token 失效，且可单独撤销。服务器只保存 SHA-256 验证值。手机使用 `WhenUnlockedThisDeviceOnly` Keychain，不同步 iCloud、不随备份迁移；密码不持久保存。旧服务器使用原有登录有效期。

设备 Token 允许创建和控制任务、与主 Agent 对话，因此有触发远程执行的权限。设置、凭据、文件管理、归档及签发新 Token 等接口不在其权限范围内。

Debug 构建支持已配对 USB 传入 App 私有 Caches 的一次性 `ARTEXDeviceEnrollment.json`。源必须已在 App 中配置且地址匹配，导入后删除临时文件。仓库排除此类文件、设备回执、签名证书及构建产物。

## 工程目录

- `App/`：SwiftUI 页面、状态、Keychain 和缓存。
- `Sources/ARTEXCore/`：模型、API、URL 检查与 SSE 解析。
- `Tests/`、`UITests/`：核心测试和原生交互测试。
- `PRD.md`、`DEVELOPMENT.md`：产品目标和验收范围。
- 仓库根目录 `db/mobile_tokens*`、`server/mobile_tokens*`：后端设备 Token 实现与测试。

大数据量、全部辅助功能、切网及长期 SSE 场景尚未全部验收。当前提供源码和开发构建，不提供 TestFlight 分发。
