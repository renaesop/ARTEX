import SwiftUI
import Observation

@MainActor @Observable final class FindingsModel {
    var items: [FindingRecord] = []
    var query = ""
    var severity = ""
    var status = ""
    var taskFilter = ""
    var sort = "severity"
    var page = 0
    var total = 0
    var loading = false
    var error = ""
    var generation = 0
    var cachedAt: Date?
    var identity: String { [query, severity, status, taskFilter, sort].joined(separator: "|") }
    func load(_ app: AppStore, source: RemoteSource, taskID: String?, reset: Bool) async {
        if !reset && loading { return }
        if reset { generation += 1; page = 0; items = []; total = 0 }
        let revision = generation; loading = true
        defer { if revision == generation { loading = false } }
        let next = reset ? 1 : page + 1
        let params = ["page": String(next), "limit": "40", "q": query, "severity": severity, "status": status, "task_id": taskID ?? taskFilter, "sort": sort]
        let key = source.id.uuidString + ":findings:" + (taskID ?? "") + ":" + identity
        if reset, let cached = app.local.load(Snapshot<[FindingRecord]>.self, key: key) { items = cached.value; cachedAt = cached.savedAt }
        do {
            let response = try await app.fetch(source, "exploration/findings", query: params)
            try Task.checkCancellation(); guard revision == generation else { return }
            let incoming = try response["items"].array.map(FindingRecord.init)
            if reset { items = incoming } else { let existing = Set(items.map(\.id)); items += incoming.filter { !existing.contains($0.id) } }
            total = response["total"].int; page = next; error = ""; cachedAt = nil
            if reset && !app.demoMode { try app.local.save(Snapshot(value: items, savedAt: Date()), key: key) }
        } catch is CancellationError {} catch { if revision == generation { self.error = app.report(error, source: source) } }
    }
}

struct FindingsView: View {
    let source: RemoteSource?
    let taskID: String?
    @Environment(AppStore.self) private var app
    @Environment(\.scenePhase) private var scene
    @State private var model = FindingsModel()
    @State private var visible = false
    @State private var filters = false
    var body: some View {
        Group {
            if let source {
                if !app.hasCredential(source) { SignInPrompt(source: source) }
                else {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 13) {
                            VStack(alignment: .leading, spacing: 7) {
                                if taskID == nil { HStack(alignment: .firstTextBaseline) { Text("Findings").compact(25, weight: .bold); Spacer(); Text(app.demoMode ? "DEMO" : "\(model.total) 项发现").compact(11, weight: .medium).foregroundStyle(.secondary).tracking(1) }.padding(.top, 8); Text("已发现的问题与证据").compact(11).foregroundStyle(.secondary).padding(.bottom, 7) }
                                SearchInput(placeholder: "搜索发现、资产或关键词", text: $model.query)
                                FilterStrip(value: $model.severity, options: [("", "全部"), ("critical", "Critical"), ("high", "High"), ("medium", "Medium"), ("low", "Low")])
                                HStack {
                                    Button { filters = true } label: { Label(model.status.isEmpty && model.taskFilter.isEmpty ? "筛选" : "已筛选", systemImage: "line.3.horizontal.decrease") }.frame(minHeight: 44)
                                    Spacer()
                                    Menu { Picker("排序", selection: $model.sort) { Text("严重程度优先").tag("severity"); Text("最新发现优先").tag("time") } } label: { Label(model.sort == "severity" ? "严重程度" : "最新发现", systemImage: "arrow.up.arrow.down") }.frame(minHeight: 44)
                                }.compact(11)
                            }
                        if let date = model.cachedAt { Text("本机缓存 · \(date.formatted(date: .abbreviated, time: .shortened))").compact(11).foregroundStyle(.secondary) }
                        if !model.error.isEmpty { InlineError(message: model.error) { Task { await model.load(app, source: source, taskID: taskID, reset: model.page == 0) } } }
                        LazyVStack(spacing: 0) {
                            ForEach(model.items) { item in NavigationLink(value: item) { FindingRow(item: item).padding(.horizontal, 14).padding(.vertical, 12).contentShape(Rectangle()) }.buttonStyle(.plain).accessibilityIdentifier("finding-\(item.id)"); if item.id != model.items.last?.id { Divider().padding(.leading, 14) } }
                        }.background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20))
                            if model.loading { ProgressView("加载 Findings…") }
                            if model.items.count < model.total && !model.loading { Button("加载更多 · \(model.items.count) / \(model.total)") { Task { await model.load(app, source: source, taskID: taskID, reset: false) } }.frame(minHeight: 44) }
                            if model.items.isEmpty && !model.loading && model.error.isEmpty { ContentUnavailableView("暂无匹配的 Findings", systemImage: "checkmark.shield") }
                        }.padding(.horizontal, 18).padding(.bottom, 18)
                    }.background(Color(.systemGroupedBackground)).refreshable { await model.load(app, source: source, taskID: taskID, reset: true) }
                }
            } else { ContentUnavailableView("添加远程源", systemImage: "server.rack") }
        }
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: $filters) { NavigationStack { Form { Picker("处理状态", selection: $model.status) { Text("全部状态").tag(""); ForEach(["pending", "in_progress", "confirmed", "resolved", "fixed", "false_positive", "ignored", "duplicate", "risk_accepted"], id: \.self) { Text(StatusLabel.text($0)).tag($0) } }; if taskID == nil { TextField("按 Task ID 筛选", text: $model.taskFilter).keyboardType(.numberPad) }; Button("清除筛选") { model.status = ""; model.taskFilter = "" } }.navigationTitle("筛选 Findings").navigationBarTitleDisplayMode(.inline).toolbar { ToolbarItem(placement: .confirmationAction) { Button("完成") { filters = false } } } }.presentationDetents([.medium, .large]) }
        .toolbar { if taskID == nil { ToolbarItem(placement: .topBarLeading) { SourceMenu() } } }
        .navigationDestination(for: FindingRecord.self) { finding in if let source { FindingDetailView(source: source, finding: finding) } }
        .onAppear { visible = true }.onDisappear { visible = false }
        .task(id: "\(model.identity)-\(visible)-\(scene)-\(app.authRevision)") {
            guard visible, scene == .active, let source, app.hasCredential(source) else { return }
            do { try await Task.sleep(for: .milliseconds(250)) } catch { return }
            await model.load(app, source: source, taskID: taskID, reset: true)
        }
    }
}

struct FindingRow: View {
    let item: FindingRecord
    var body: some View { VStack(alignment: .leading, spacing: 4) {
        HStack { StatusBadge(value: item.severity); Spacer(); Text(StatusLabel.text(item.status)).compact(11).foregroundStyle(.secondary) }
        Text(item.name).compact(13, weight: .semibold).lineLimit(2)
        if !item.assetText.isEmpty { Text(item.assetText).compact(11).foregroundStyle(.secondary).lineLimit(1) }
        HStack { Text(item.taskID.isEmpty ? "原任务不可用" : "Task #\(item.taskID)"); Spacer(); Text(displayTime(item.raw["ts"].string)); Image(systemName: "chevron.right").font(.system(size: 9, weight: .semibold)).foregroundStyle(.tertiary) }.compact(11).foregroundStyle(.secondary)
    }.padding(.vertical, 3) }
}

struct FindingDetailView: View {
    let source: RemoteSource
    @State var finding: FindingRecord
    @Environment(AppStore.self) private var app
    @State private var error = ""
    @State private var task: TaskRecord?
    var body: some View {
        List {
            Section {
                Text(finding.name).compact(19, weight: .semibold).textSelection(.enabled)
                HStack { StatusBadge(value: finding.severity); StatusBadge(value: finding.status) }
                Text("\(source.name) · Finding #\(finding.id)").compact(11).foregroundStyle(.secondary)
            }
            InlineError(message: error) { Task { await load() } }
            Section("摘要") { MarkdownContent(text: finding.raw["summary"].string) }
            if !finding.assetText.isEmpty { Section("受影响资产") { Text(finding.assetText).compact(12).textSelection(.enabled) } }
            Section("证据") { MarkdownContent(text: finding.raw["evidence"].string.isEmpty ? "暂无文本证据" : finding.raw["evidence"].string); LabeledContent("关联流量", value: "\(finding.raw["traffic_count"].int) 条").compact(12) }
            Section("报告") {
                if finding.raw["report_stale"].bool { Label("报告尚未包含最新证据", systemImage: "exclamationmark.clock").compact(12).foregroundStyle(.orange) }
                MarkdownContent(text: finding.raw["report"].string.isEmpty ? "暂无报告" : finding.raw["report"].string)
            }
            Section("来源") {
                if finding.raw["inherited"].bool { Text("继承发现 · 原始 Task #\(finding.raw["source_task_id"].string)").compact(12) }
                if finding.taskID.isEmpty { Text("原任务已删除或不可用，Finding 仍可阅读。").compact(12).foregroundStyle(.secondary) }
                else { Button("查看 Task #\(finding.taskID)", systemImage: "checklist") { Task { do { task = try TaskRecord(await app.fetch(source, "tasks/\(finding.taskID)")) } catch { self.error = app.report(error, source: source) } } }.frame(minHeight: 44) }
            }
        }.listStyle(.insetGrouped).navigationTitle("Finding").navigationBarTitleDisplayMode(.inline).toolbar(.hidden, for: .tabBar)
        .refreshable { await load() }.task { await load() }
        .navigationDestination(item: $task) { TaskDetailView(source: source, task: $0) }
    }
    func load() async {
        let key = source.id.uuidString + ":finding:" + finding.id
        if let cached = app.local.load(FindingRecord.self, key: key) { finding = cached }
        do { let raw = try await app.fetch(source, "exploration/findings/\(finding.id)"); try Task.checkCancellation(); finding = try FindingRecord(raw); error = ""; if !app.demoMode { try app.local.save(finding, key: key) } } catch is CancellationError {} catch { self.error = app.report(error, source: source) }
    }
}

struct MarkdownContent: View {
    let text: String
    private var blocks: [(code: Bool, text: String)] {
        let parts = text.components(separatedBy: "```")
        return parts.enumerated().flatMap { index, value -> [(Bool, String)] in
            if index % 2 == 1 { let code = value.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false).dropFirst().joined(separator: "\n"); return [(true, code)] }
            return value.components(separatedBy: "\n\n").filter { !$0.isEmpty }.map { (false, $0) }
        }
    }
    var body: some View { VStack(alignment: .leading, spacing: 10) { ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
        if block.code { ScrollView(.horizontal) { Text(block.text).font(.system(.caption, design: .monospaced)).padding(10).textSelection(.enabled) }.background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10)) }
        else if block.text.hasPrefix("#") { Text(block.text.trimmingCharacters(in: CharacterSet(charactersIn: "# "))).compact(14, weight: .semibold).textSelection(.enabled) }
        else { Text((try? AttributedString(markdown: block.text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(block.text)).compact(13).textSelection(.enabled) }
    } }.frame(maxWidth: .infinity, alignment: .leading).environment(\.openURL, OpenURLAction { url in guard ["https", "http"].contains(url.scheme?.lowercased() ?? "") else { return .discarded }; return .systemAction }) }
}
