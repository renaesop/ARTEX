import SwiftUI

struct SourceMenu: View {
    @Environment(AppStore.self) private var store
    @State private var manage = false
    var body: some View {
        Menu {
            ForEach(store.sources) { source in Button { store.select(source.id) } label: { Label("\(source.name) · \(store.sourceStates[source.id] ?? "未检查")", systemImage: store.selectedID == source.id ? "checkmark" : "server.rack") } }
            Divider()
            Button("管理远程源", systemImage: "slider.horizontal.3") { manage = true }
        } label: { HStack(spacing: 7) { Circle().fill(store.selected.flatMap { store.sourceStates[$0.id] } == "可连接" ? Color.green : Color.secondary).frame(width: 6, height: 6); Text(store.selected?.name ?? "添加远程源").lineLimit(1); Image(systemName: "chevron.down").font(.system(size: 10, weight: .semibold)).foregroundStyle(.secondary) }.compact(12, weight: .medium).foregroundStyle(.primary) }
        .accessibilityIdentifier("source-menu")
        .tint(.primary)
        .sheet(isPresented: $manage) { NavigationStack { SourcesView() } }
    }
}

struct SourcesView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var editing: RemoteSource?
    @State private var login: RemoteSource?
    @State private var removing: RemoteSource?
    @State private var checking: UUID?
    @State private var message = ""
    var body: some View {
        List {
            if !message.isEmpty { InlineError(message: message) }
            Section {
                ForEach(store.sources) { source in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack { Text(source.name).compact(14, weight: .semibold); Spacer(); Text(store.sourceStates[source.id] ?? "未检查").compact(11).foregroundStyle(.secondary) }
                        Text(source.address).compact(11).foregroundStyle(.secondary).textSelection(.enabled)
                        if let version = source.version { Text("服务器 \(version)").compact(11).foregroundStyle(.secondary) }
                        HStack {
                            Button("编辑") { editing = source }
                            Button(store.hasCredential(source) ? "重新登录" : "登录") { login = source }
                            Button { Task { checking = source.id; defer { checking = nil }; do { try await store.check(source); message = "连接成功" } catch { message = store.report(error, source: source) } } } label: { if checking == source.id { ProgressView() } else { Text("测试连接") } }.disabled(checking != nil)
                            Spacer()
                            Menu {
                                Button("清除本机缓存") { store.local.clear(source: source.id); message = "已清除 \(source.name) 的本机缓存" }
                                Button("退出并撤销本机 Token", role: .destructive) { Task { do { try await store.logout(source) } catch { message = error.localizedDescription } } }
                                Button("移除远程源", role: .destructive) { removing = source }
                            } label: { Image(systemName: "ellipsis").frame(width: 44, height: 44) }
                        }.compact(12).buttonStyle(.borderless).frame(minHeight: 44)
                    }.padding(.vertical, 3)
                }.onMove { store.move(from: $0, to: $1) }
            } footer: { Text("通过 iPhone 上的 Tailscale 连接访问。凭据保存在本机 Keychain，不随 iCloud 同步；密码不持久保存。") }
            Button("添加远程源", systemImage: "plus") { editing = RemoteSource(name: "", address: "https://") }
            #if DEBUG
            Section { Toggle("演示模式（不连接服务器）", isOn: Binding(get: { store.demoMode }, set: { store.setDemoMode($0) })) }
            #endif
        }
        .navigationTitle("远程源").navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .topBarLeading) { Button("完成") { dismiss() } }; ToolbarItem(placement: .topBarTrailing) { EditButton() } }
        .sheet(item: $editing) { source in NavigationStack { SourceEditor(source: source) } }
        .sheet(item: $login) { source in NavigationStack { LoginView(source: source) } }
        .confirmationDialog("移除 \(removing?.name ?? "远程源")？", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }), titleVisibility: .visible) {
            Button("移除本机配置", role: .destructive) { if let source = removing { do { try store.remove(source) } catch { message = error.localizedDescription } }; removing = nil }
        } message: { Text("清除该源的本机凭据、草稿和缓存。不会删除服务器数据。已签发的 App Token 请先选择“退出并撤销”，移除本机配置不等同于服务端撤销。") }
    }
}

struct SourceEditor: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State var source: RemoteSource
    @State private var error = ""
    var body: some View {
        Form {
            Section("远程源") {
                TextField("名称", text: $source.name)
                TextField("HTTPS 地址", text: $source.address).textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                TextField("连接说明", text: $source.note)
            }
            Text("修改地址会清除本机凭据与缓存，并要求重新登录。").compact(12).foregroundStyle(.secondary)
            InlineError(message: error)
        }.navigationTitle("源配置").navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }; ToolbarItem(placement: .confirmationAction) { Button("保存") { do { try store.save(source); dismiss() } catch { self.error = error.localizedDescription } }.disabled(source.name.trimmingCharacters(in: .whitespaces).isEmpty) } }
    }
}

struct LoginView: View {
    let source: RemoteSource
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var username = "ARTEX"
    @State private var password = ""
    @State private var token = ""
    @State private var tokenMode = false
    @State private var busy = false
    @State private var error = ""
    var body: some View {
        Form {
            Section { Text(source.name).font(.headline); Text(source.address).compact(12).foregroundStyle(.secondary) }
            Picker("登录方式", selection: $tokenMode) { Text("账号登录").tag(false); Text("已有 Token").tag(true) }.pickerStyle(.segmented)
            if tokenMode { SecureField("App Token", text: $token).textInputAutocapitalization(.never).autocorrectionDisabled() }
            else { TextField("账号", text: $username).textInputAutocapitalization(.never).autocorrectionDisabled().textContentType(.username); SecureField("密码", text: $password).textContentType(.password) }
            Section {
                Button { Task { busy = true; error = ""; defer { busy = false }; do { try await store.connect(source, username: username, password: password, suppliedToken: tokenMode ? token : ""); password = ""; token = ""; dismiss() } catch { self.error = store.report(error, source: source) } } } label: { HStack { if busy { ProgressView() }; Text(busy ? "正在连接…" : "登录并验证") }.frame(maxWidth: .infinity, minHeight: 44) }.disabled(busy || (tokenMode ? token.isEmpty : password.isEmpty))
            } footer: { Text("支持专用 Token 的服务器会为此设备签发 90 天凭据，可随时撤销；旧版本暂时使用服务器的登录有效期。") }
            InlineError(message: error)
        }.navigationTitle("登录 \(source.name)").navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("关闭") { dismiss() }.disabled(busy) } }.interactiveDismissDisabled(busy)
    }
}

struct SignInPrompt: View {
    let source: RemoteSource
    @State private var login = false
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 7) { Text("ARTEX").compact(25, weight: .bold).tracking(-0.5); Text("任务、会话与 Findings，随时掌握。").compact(12).foregroundStyle(.secondary) }.padding(.top, 12)
                VStack(alignment: .leading, spacing: 20) {
                    HStack(spacing: 12) {
                        Image(systemName: "server.rack").font(.system(size: 22, weight: .light)).foregroundStyle(.blue).frame(width: 48, height: 48).background(.blue.opacity(0.07), in: RoundedRectangle(cornerRadius: 15))
                        VStack(alignment: .leading, spacing: 4) { Text(source.name).compact(16, weight: .semibold); Label("Tailscale 私网连接", systemImage: "lock.shield").compact(11).foregroundStyle(.secondary) }
                    }
                    Text(source.address.replacingOccurrences(of: "https://", with: "")).font(.system(.caption, design: .monospaced)).foregroundStyle(.secondary).textSelection(.enabled)
                    Divider()
                    VStack(alignment: .leading, spacing: 9) { Text("登录后开始").compact(14, weight: .semibold); Text("打开 Tailscale 并连接网络，然后登录此服务器。各远程源的会话和凭据独立保存。").compact(12).foregroundStyle(.secondary).lineSpacing(3) }
                    Button { login = true } label: { HStack { Text("连接服务器"); Spacer(); Image(systemName: "arrow.right") }.compact(13, weight: .semibold).padding(.horizontal, 16).frame(height: 46) }.buttonStyle(.plain).foregroundStyle(.white).background(.blue, in: Capsule())
                }.padding(20).background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 24))
                HStack(alignment: .top, spacing: 7) { Image(systemName: "key.horizontal"); Text("登录凭据仅保存在这台 iPhone 的 Keychain。") }.compact(11).foregroundStyle(.secondary).padding(.horizontal, 4)
            }.padding(.horizontal, 20).padding(.bottom, 24)
        }.background(Color(.systemGroupedBackground)).sheet(isPresented: $login) { NavigationStack { LoginView(source: source) } }
    }
}
