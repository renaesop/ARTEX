import SwiftUI
import Observation

@MainActor @Observable final class SessionsModel {
    var sessions: [SessionDescriptor] = []
    var current = 0
    var query = ""
    var filter = ""
    var loading = false
    var initialized = false
    var hasMore = true
    var cursor = 0
    var error = ""
    var searchingAll = false
    var filtered: [SessionDescriptor] { sessions.filter { $0.id != "main:\(current)" && (query.isEmpty || "\($0.id) \($0.title) \($0.role)".localizedCaseInsensitiveContains(query)) && (filter.isEmpty || (filter == "main" && $0.segment != nil) || (filter == "worker" && $0.id.hasPrefix("intent:")) || (filter == "running" && ["running", "claimed"].contains($0.status))) } }
    func load(_ app: AppStore, source: RemoteSource, task: String, reset: Bool = false) async {
        guard !loading else { return }; loading = true; defer { loading = false }
        do {
            if !initialized || reset {
                let main = try await app.fetch(source, "exploration/main-sessions", query: ["task": task]); try Task.checkCancellation()
                current = main["current"].int
                sessions = main["sessions"].array.map { .main($0["seq"].int) }
                if !sessions.contains(where: { $0.id == "main:\(current)" }) { sessions.insert(.main(current), at: 0) }
                sessions += [.init(id: "plan", title: "Planner", role: "Planner"), .init(id: "system", title: "系统事件", role: "System")]
                cursor = 0; hasMore = true; initialized = true
            }
            guard hasMore else { return }
            let page = try await app.fetch(source, "exploration/intents", query: ["task": task, "page": "1", "before": String(cursor), "limit": "60"])
            try Task.checkCancellation()
            let nodes = page["items"].array
            let known = Set(sessions.map(\.id)); sessions += nodes.map(SessionDescriptor.worker).filter { !known.contains($0.id) }
            let next = nodes.map { $0["id"].int }.filter { $0 > 0 }.min() ?? 0
            if page["has_more"].bool && (next == 0 || (cursor != 0 && next >= cursor)) { throw APIError.http(500, "会话分页游标未前进，请重试或更新服务器。") }
            cursor = next; hasMore = page["has_more"].bool; error = ""
        } catch is CancellationError {} catch { self.error = app.report(error, source: source) }
    }
    func searchAll(_ app: AppStore, source: RemoteSource, task: String) async {
        guard !query.isEmpty || !filter.isEmpty else { return }
        searchingAll = true; defer { searchingAll = false }
        while hasMore && !Task.isCancelled {
            if loading { do { try await Task.sleep(for: .milliseconds(100)) } catch { return }; continue }
            await load(app, source: source, task: task)
            if !error.isEmpty { break }
        }
    }
}

struct SessionsView: View {
    let source: RemoteSource
    let taskID: String
    let taskName: String
    @Environment(AppStore.self) private var app
    @State private var model = SessionsModel()
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                VStack(spacing: 5) {
                SearchInput(placeholder: "搜索会话标题或 Worker ID", text: $model.query).accessibilityIdentifier("session-search")
                FilterStrip(value: $model.filter, options: [("", "全部"), ("running", "执行中"), ("worker", "Worker"), ("main", "主对话")])
                HStack { Text(model.hasMore ? "已加载 \(model.sessions.count) 个会话" : "共 \(model.sessions.count) 个会话"); Spacer(); if model.searchingAll { ProgressView(); Text("搜索全部会话…") } }.compact(11).foregroundStyle(.secondary)
                }
            if !model.error.isEmpty { InlineError(message: model.error) { Task { await model.load(app, source: source, task: taskID) } } }
            if model.initialized {
                Text("主对话").compact(11, weight: .medium).foregroundStyle(.secondary).padding(.top, 3)
                NavigationLink(value: SessionDescriptor.main(model.current)) { SessionRow(session: .main(model.current), main: true).padding(.horizontal, 13).padding(.vertical, 12).contentShape(Rectangle()) }.buttonStyle(.plain).background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 18)).accessibilityIdentifier("main-chat")
                Text(model.query.isEmpty && model.filter.isEmpty ? "执行会话与历史" : (model.hasMore ? "当前匹配（仍在搜索）" : "全部匹配会话")).compact(11, weight: .medium).foregroundStyle(.secondary).padding(.top, 5)
                LazyVStack(spacing: 0) {
                    ForEach(model.filtered) { session in NavigationLink(value: session) { SessionRow(session: session).padding(.horizontal, 13).padding(.vertical, 11).contentShape(Rectangle()) }.buttonStyle(.plain).accessibilityIdentifier(session.id); if session.id != model.filtered.last?.id { Divider().padding(.leading, 52) } }
                    if model.filtered.isEmpty && !model.loading { Text(model.hasMore ? "当前已加载的会话中没有匹配项" : "没有匹配的会话").compact(12).foregroundStyle(.secondary) }
                }.background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 18))
            }
            if model.loading { ProgressView("加载会话…") }
            if model.hasMore && !model.loading { Button("加载更多会话") { Task { await model.load(app, source: source, task: taskID) } }.compact(12).frame(maxWidth: .infinity, minHeight: 44).buttonStyle(.glass) }
            }.padding(.horizontal, 16).padding(.top, 4).padding(.bottom, 18)
        }.background(Color(.systemGroupedBackground))
        .refreshable { await model.load(app, source: source, task: taskID, reset: true) }
        .task { if !model.initialized { await model.load(app, source: source, task: taskID) } }
        .task(id: model.query + "|" + model.filter) { do { try await Task.sleep(for: .milliseconds(300)) } catch { return }; await model.searchAll(app, source: source, task: taskID) }
        .navigationDestination(for: SessionDescriptor.self) { session in SessionDetailView(source: source, taskID: taskID, taskName: taskName, session: session, currentMain: model.current) }
    }
}

struct SessionRow: View {
    let session: SessionDescriptor
    var main = false
    var icon: String { session.segment != nil ? "bubble.left.and.bubble.right" : session.id == "plan" ? "point.3.connected.trianglepath.dotted" : session.id == "system" ? "gearshape" : "terminal" }
    var body: some View { HStack(spacing: 9) {
        Image(systemName: icon).font(.system(size: 15)).foregroundStyle(session.segment != nil ? .blue : .green).frame(width: 30, height: 30).background(Color.blue.opacity(0.07), in: RoundedRectangle(cornerRadius: 10))
        VStack(alignment: .leading, spacing: 3) { Text(main ? "主对话" : session.title).compact(13, weight: .semibold).lineLimit(2); Text(main ? "给 Task 发消息或 Hint" : "\(session.role)\(session.status.isEmpty ? "" : " · " + StatusLabel.text(session.status))\(session.inherited ? " · 继承只读" : "")").compact(11).foregroundStyle(.secondary) }
        Spacer(minLength: 2)
        if main { Text("可对话").compact(11).foregroundStyle(.green) }
        Image(systemName: "chevron.right").font(.system(size: 10, weight: .medium)).foregroundStyle(.tertiary)
    }.padding(.vertical, 1) }
}
