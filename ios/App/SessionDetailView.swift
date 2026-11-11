import SwiftUI
import Observation

private struct ChatDraft: Codable { var text: String; var uncertain: Bool }

@MainActor @Observable final class TranscriptModel {
    var items: [ActivityRecord] = []
    var details: [String: String] = [:]
    var loading = false
    var olderLoading = false
    var hasMore = false
    var earliest = 0
    var cursor = 0
    var busy = false
    var statusKnown = false
    var sending = false
    var stopping = false
    var uncertain = false
    var draft = ""
    var error = ""
    var connection = "正在连接"
    var delivery = ""
    var newMessages = 0
    var initialized = false
    var detailErrors: [String: String] = [:]
    private var restoredDraft = false
    private func key(_ source: RemoteSource, _ task: String, _ session: SessionDescriptor) -> String { source.id.uuidString + ":chat:" + task + ":" + session.id }
    func saveDraft(_ app: AppStore, _ source: RemoteSource, _ task: String, _ session: SessionDescriptor) {
        do { try app.local.save(ChatDraft(text: draft, uncertain: uncertain), key: key(source, task, session) + ":draft") } catch { self.error = "草稿未能保存：\(error.localizedDescription)" }
    }
    func load(_ app: AppStore, source: RemoteSource, task: String, session: SessionDescriptor, older: Bool = false) async -> Bool {
        if older { guard !olderLoading, hasMore else { return false }; olderLoading = true } else { guard !loading else { return false }; loading = true }
        defer { if older { olderLoading = false } else { loading = false } }
        let storage = key(source, task, session)
        if !restoredDraft {
            if let saved = app.local.load(ChatDraft.self, key: storage + ":draft") { draft = saved.text; uncertain = saved.uncertain }
            restoredDraft = true
        }
        if !initialized, let cached = app.local.load(Snapshot<[ActivityRecord]>.self, key: storage) { items = cached.value; connection = "本机缓存 · \(cached.savedAt.formatted(date: .abbreviated, time: .shortened))" }
        do {
            let result = try await app.fetch(source, "exploration/activity/history", query: ["task": task, "session": session.id, "before": String(older ? earliest : 0), "limit": "60"])
            try Task.checkCancellation()
            let incoming = try result["items"].array.map(ActivityRecord.init)
            items = initialized ? ActivityRecord.merge(items, incoming) : incoming
            if older || !initialized { earliest = result["earliest_cursor"].int; hasMore = result["has_more"].bool }
            if !initialized { cursor = result["snapshot_cursor"].int }
            initialized = true; error = ""
            if !app.demoMode { try app.local.save(Snapshot(value: Array(items.suffix(200)), savedAt: Date()), key: storage) }
            return true
        } catch is CancellationError {} catch { self.error = app.report(error, source: source) }
        return false
    }
    func run(_ app: AppStore, source: RemoteSource, task: String, session: SessionDescriptor) async {
        guard await load(app, source: source, task: task, session: session) else { return }
        if app.demoMode { busy = false; statusKnown = true; connection = "演示模式"; return }
        await withTaskGroup(of: Void.self) { group in
            if !session.inherited { group.addTask { await self.stream(app, source: source, task: task, session: session) } }
            if session.writable { group.addTask { await self.pollStatus(app, source: source, task: task) } }
            await group.waitForAll()
        }
    }
    private func stream(_ app: AppStore, source: RemoteSource, task: String, session: SessionDescriptor) async {
        var delay: Double = 2
        while !Task.isCancelled {
            do {
                connection = "实时连接中"
                try await app.client(source).stream(task: task, since: cursor) { [weak self] record in await self?.receive(record, session: session) }
                if !Task.isCancelled { connection = "连接断开，正在补齐记录" }
            } catch is CancellationError { break } catch {
                if Task.isCancelled { break }
                connection = "连接中断，正在重连"
                if (error as? APIError) == .unauthenticated { self.error = app.report(error, source: source); return }
            }
            do { try await Task.sleep(for: .seconds(delay)) } catch { break }; delay = min(delay * 2, 30)
        }
    }
    private func receive(_ record: ActivityRecord, session: SessionDescriptor) {
        cursor = max(cursor, record.seq)
        connection = "实时连接中"
        guard record.sessionKey == session.id, !record.raw["inherited"].bool else { return }
        let exists = items.contains { $0.id == record.id }
        items = ActivityRecord.merge(items, [record])
        if !exists && record.kind != "usage" { newMessages += 1 }
    }
    func pollStatus(_ app: AppStore, source: RemoteSource, task: String) async {
        while !Task.isCancelled {
            do { let result = try await app.fetch(source, "tasks/\(task)/chat/status"); try Task.checkCancellation(); busy = result["running"].bool; statusKnown = true; if !busy { stopping = false } }
            catch is CancellationError { break } catch { statusKnown = false; self.error = app.report(error, source: source); if (error as? APIError) == .unauthenticated { return } }
            do { try await Task.sleep(for: .seconds(3)) } catch { break }
        }
    }
    func send(_ app: AppStore, source: RemoteSource, task: String, session: SessionDescriptor) async {
        guard let segment = session.segment, session.writable, !sending, !busy, statusKnown, !uncertain, !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let text = draft; sending = true; delivery = "发送中"; error = ""
        defer { sending = false; saveDraft(app, source, task, session) }
        do {
            let result = try await app.fetch(source, "chat", method: "POST", query: ["task": task], body: .object(["message": .string(text), "seg": .integer(Int64(segment))]))
            if draft == text { draft = "" }
            delivery = result["status"].string == "accepted" ? "已接收，主 Agent 处理中" : "已收到服务器回复"
            busy = result["status"].string == "accepted"
            _ = await load(app, source: source, task: task, session: session)
        } catch {
            self.error = app.report(error, source: source)
            uncertain = (error as? APIError) == .uncertain
            delivery = uncertain ? "结果待确认，草稿已保留" : "未发送，草稿已保留"
            if let status = try? await app.fetch(source, "tasks/\(task)/chat/status") { busy = status["running"].bool; statusKnown = true }
            _ = await load(app, source: source, task: task, session: session)
            self.error = error.localizedDescription
        }
    }
    func stop(_ app: AppStore, source: RemoteSource, task: String) async {
        guard !stopping else { return }; stopping = true
        do { let response = try await app.fetch(source, "tasks/\(task)/chat/stop", method: "POST", body: .object([:])); delivery = response["status"].string == "idle" ? "当前没有正在生成的回复" : "正在停止当前回复，Task 状态不变"; if response["status"].string == "idle" { busy = false; stopping = false } }
        catch { stopping = false; self.error = app.report(error, source: source) }
    }
    func detail(_ record: ActivityRecord, app: AppStore, source: RemoteSource, task: String) async {
        guard details[record.id] == nil else { return }
        if !record.detail.isEmpty { details[record.id] = record.detail; return }
        do { let taskID = record.raw["source_task_id"].string.isEmpty ? task : record.raw["source_task_id"].string; let r = try await app.fetch(source, "exploration/activity/\(record.seq)", query: ["task": taskID]); details[record.id] = r["detail"].string.isEmpty ? record.summary : r["detail"].string; detailErrors[record.id] = nil }
        catch { detailErrors[record.id] = app.report(error, source: source) }
    }
}

struct SessionDetailView: View {
    let source: RemoteSource
    let taskID: String
    let taskName: String
    let session: SessionDescriptor
    let currentMain: Int
    @Environment(AppStore.self) private var app
    @Environment(\.scenePhase) private var scene
    @State private var model = TranscriptModel()
    @State private var visible = false
    @State private var firstScroll = true
    @State private var forwarded = false
    @State private var retry = 0
    @State private var mainSession: SessionDescriptor?
    @FocusState private var composerFocused: Bool
    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    VStack(alignment: .leading, spacing: 5) {
                        HStack { Text(session.title).compact(19, weight: .semibold); Spacer(); Text(session.writable ? "可对话" : "只读").compact(11).foregroundStyle(session.writable ? .green : .secondary) }
                        Text("\(source.name) · #\(taskID) · \(taskName)").compact(11).foregroundStyle(.secondary).lineLimit(2)
                        HStack(spacing: 5) { Circle().fill(model.initialized ? Color.green : Color.secondary).frame(width: 4, height: 4); Text(model.connection) }.compact(11).foregroundStyle(.tertiary)
                    }.padding(.bottom, 3)
                    if !model.error.isEmpty { InlineError(message: model.error) { retry += 1 } }
                    if model.hasMore { Button { let anchor = model.items.first?.id; Task { _ = await model.load(app, source: source, task: taskID, session: session, older: true); if let anchor { proxy.scrollTo(anchor, anchor: .top) } } } label: { HStack { Spacer(); if model.olderLoading { ProgressView() } else { Text("加载更早记录") }; Spacer() }.frame(minHeight: 44) }.disabled(model.olderLoading) }
                    if model.loading && model.items.isEmpty { ProgressView("加载会话…").frame(maxWidth: .infinity) }
                    ForEach(model.items.filter { $0.kind != "usage" }) { record in ActivityView(record: record, model: model, source: source, taskID: taskID).id(record.id) }
                    if model.items.isEmpty && model.initialized { ContentUnavailableView("暂无会话记录", systemImage: "bubble.left") }
                    Color.clear.frame(height: 1).id("latest")
                }.padding(16)
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                VStack(spacing: 7) {
                    if model.newMessages > 0 { Button("\(model.newMessages) 条新活动 · 回到最新") { proxy.scrollTo("latest", anchor: .bottom); model.newMessages = 0 }.compact(12).frame(minHeight: 44).buttonStyle(.glass) }
                    if session.writable { composer }
                    else { Button("给主对话发 Hint", systemImage: "bubble.left.and.text.bubble.right") { let target = SessionDescriptor.main(currentMain); let key = source.id.uuidString + ":chat:" + taskID + ":" + target.id + ":draft"; if app.local.load(ChatDraft.self, key: key)?.text.isEmpty ?? true { try? app.local.save(ChatDraft(text: "关于 \(session.title)：", uncertain: false), key: key) }; mainSession = target }.buttonStyle(.glass).frame(minHeight: 44).padding(.bottom, 8) }
                }.padding(.horizontal, 12).padding(.bottom, 6)
            }
            .onChange(of: model.initialized) { _, ready in if ready && firstScroll { firstScroll = false; proxy.scrollTo("latest", anchor: .bottom) } }
            .onChange(of: model.sending) { old, sending in if old && !sending && model.draft.isEmpty { proxy.scrollTo("latest", anchor: .bottom); model.newMessages = 0 } }
        }
        .background(Color(.systemGroupedBackground)).navigationTitle("Session").navigationBarTitleDisplayMode(.inline).toolbar(.hidden, for: .tabBar)
        .navigationDestination(item: $mainSession) { SessionDetailView(source: source, taskID: taskID, taskName: taskName, session: $0, currentMain: currentMain) }
        .onAppear { visible = true }.onDisappear { visible = false; model.saveDraft(app, source, taskID, session) }
        .onChange(of: model.draft) { _, _ in model.saveDraft(app, source, taskID, session) }
        .task(id: "\(visible)-\(scene)-\(retry)") { guard visible, scene == .active else { return }; await model.run(app, source: source, task: taskID, session: session) }
    }
    var composer: some View {
        VStack(alignment: .leading, spacing: 5) {
            if model.uncertain { Text("结果待确认：先核对历史与当前状态。").compact(11).foregroundStyle(.orange); Button("已核对，允许再次发送") { model.uncertain = false; model.saveDraft(app, source, taskID, session) }.compact(12).frame(minHeight: 44) }
            HStack(alignment: .bottom, spacing: 3) {
                Menu { ForEach(["补充线索", "调整重点", "询问进展"], id: \.self) { text in Button(text) { if model.draft.isEmpty { model.draft = text == "询问进展" ? "请总结当前进展、已有发现和下一步计划。" : "\(text)：" } }.disabled(!model.draft.isEmpty) } } label: { Image(systemName: "plus").font(.system(size: 17, weight: .regular)).foregroundStyle(.secondary).frame(width: 38, height: 44) }.accessibilityLabel("Hint 快捷提示")
                TextField("发消息或 Hint…", text: $model.draft, axis: .vertical)
                    .lineLimit(1...5).compact(14).focused($composerFocused)
                    .padding(.vertical, 12).frame(minHeight: 44).contentShape(Rectangle())
                    .onTapGesture { composerFocused = true }
                    .accessibilityIdentifier("chat-input")
                if model.busy { Button { Task { await model.stop(app, source: source, task: taskID) } } label: { Image(systemName: model.stopping ? "hourglass" : "stop.fill").frame(width: 44, height: 44) }.disabled(model.stopping).accessibilityLabel("停止当前回复").accessibilityIdentifier("stop-chat") }
                else { Button { Task { await model.send(app, source: source, task: taskID, session: session) } } label: { Group { if model.sending { ProgressView() } else { Image(systemName: "arrow.up").font(.system(size: 16, weight: .semibold)) } }.foregroundStyle(.white).frame(width: 34, height: 34).background(model.draft.isEmpty ? Color.gray.opacity(0.35) : Color.blue, in: Circle()).frame(width: 44, height: 44) }.buttonStyle(.plain).disabled(model.sending || !model.statusKnown || model.uncertain || model.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty).accessibilityLabel("发送 Hint").accessibilityIdentifier("send-hint") }
            }.padding(.horizontal, 5).padding(.vertical, 4)
                .background { RoundedRectangle(cornerRadius: 27).fill(.clear).glassEffect(.regular, in: RoundedRectangle(cornerRadius: 27)).allowsHitTesting(false) }
            Text(model.delivery.isEmpty ? "发送至 \(session.title)" : model.delivery).compact(11).foregroundStyle(.secondary).lineLimit(2).padding(.horizontal, 12)
        }
    }
}

struct ActivityView: View {
    let record: ActivityRecord
    @Bindable var model: TranscriptModel
    let source: RemoteSource
    let taskID: String
    @Environment(AppStore.self) private var app
    @State private var expanded = false
    var tool: Bool { ["tool_use", "tool_result", "thinking", "round"].contains(record.kind) }
    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack { Text(record.kind == "user" ? "你" : record.raw["worker"].string == "mainagent" ? "Main Agent" : record.raw["worker"].string); Spacer(); Text(displayTime(record.raw["ts"].string)); if !tool && record.detail.isEmpty { Menu { Button("加载完整内容") { Task { await model.detail(record, app: app, source: source, task: taskID) } } } label: { Image(systemName: "ellipsis").font(.system(size: 13)).frame(width: 28, height: 24) }.accessibilityLabel("消息操作") } }.compact(11).foregroundStyle(.secondary)
            if tool {
                DisclosureGroup(isExpanded: $expanded) {
                    if let text = model.details[record.id] { MarkdownContent(text: text) }
                    else if let error = model.detailErrors[record.id] { InlineError(message: error) { Task { await model.detail(record, app: app, source: source, task: taskID) } } }
                    else { ProgressView("加载完整输出…") }
                } label: { VStack(alignment: .leading, spacing: 3) { if !record.raw["tool"].string.isEmpty { Text(record.raw["tool"].string).compact(11, weight: .semibold) }; Text(record.summary.isEmpty ? record.kind : record.summary).compact(12).lineLimit(2) }.frame(minHeight: 44) }
                .onChange(of: expanded) { _, value in if value { Task { await model.detail(record, app: app, source: source, task: taskID) } } }
            } else {
                MarkdownContent(text: model.details[record.id] ?? (record.detail.isEmpty ? record.summary : record.detail))
            }
            if record.raw["is_error"].bool { Label("执行异常", systemImage: "exclamationmark.circle").compact(11).foregroundStyle(.red) }
        }
        .padding(11).background(record.kind == "user" ? Color.blue.opacity(0.08) : Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 16))
        .padding(record.kind == "user" ? .leading : .trailing, 16)
    }
}
