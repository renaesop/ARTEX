import SwiftUI
import Observation

@MainActor @Observable final class TaskListModel {
    var items: [TaskRecord] = []
    var query = ""
    var status = ""
    var loading = false
    var error = ""
    var cachedAt: Date?
    var loaded = false
    var filtered: [TaskRecord] { items.filter { (status.isEmpty || $0.status == status) && (query.isEmpty || "\($0.id) \($0.name) \($0.description)".localizedCaseInsensitiveContains(query)) }.sorted { a, b in if a.pinned != b.pinned { return a.pinned }; return a.updated > b.updated } }
    func load(_ app: AppStore, _ source: RemoteSource) async {
        guard !loading else { return }; loading = true; defer { loading = false }
        let key = source.id.uuidString + ":tasks"
        if !loaded, let cache = app.local.load(Snapshot<[TaskRecord]>.self, key: key) { items = cache.value; cachedAt = cache.savedAt }
        do { let response = try await app.fetch(source, "tasks"); try Task.checkCancellation(); items = try response["tasks"].array.map(TaskRecord.init); loaded = true; error = ""; cachedAt = nil; if !app.demoMode { try app.local.save(Snapshot(value: items, savedAt: Date()), key: key) } }
        catch is CancellationError {} catch { self.error = app.report(error, source: source) }
    }
}

struct TasksView: View {
    @Environment(AppStore.self) private var app
    var body: some View {
        Group { if let source = app.selected { TaskListView(source: source) } else { ContentUnavailableView("添加远程源", systemImage: "server.rack", description: Text("从左上角添加 ARTEX 服务器。")) } }
        .toolbar { ToolbarItem(placement: .topBarLeading) { SourceMenu() } }
    }
}

struct TaskListView: View {
    let source: RemoteSource
    @Environment(AppStore.self) private var app
    @Environment(\.scenePhase) private var scene
    @State private var model = TaskListModel()
    @State private var creating = false
    @State private var created: TaskRecord?
    @State private var visible = false
    var body: some View {
        Group {
            if !app.hasCredential(source) { SignInPrompt(source: source) }
            else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        VStack(alignment: .leading, spacing: 7) {
                            HStack(alignment: .firstTextBaseline) { Text("Tasks").compact(25, weight: .bold); Spacer(); Text(app.demoMode ? "DEMO" : "\(model.items.count) 个任务").compact(11, weight: .medium).foregroundStyle(.secondary).tracking(1) }
                            HStack(spacing: 5) { Circle().fill(.green).frame(width: 5, height: 5); Text("\(model.items.filter { $0.status == "running" }.count) 个运行中"); Text("·"); Text("\(source.name)") }.compact(11).foregroundStyle(.secondary)
                        }.padding(.top, 8)
                        VStack(spacing: 5) {
                            SearchInput(placeholder: "搜索任务名称或 ID", text: $model.query)
                            FilterStrip(value: $model.status, options: [("", "全部"), ("running", "运行中"), ("queued", "排队中"), ("paused", "已暂停"), ("done", "已完成"), ("failed", "失败"), ("timeout", "超时")])
                        }
                        HStack { Text("最近任务"); Spacer(); Text(model.cachedAt == nil ? (model.loading ? "更新中" : "已同步") : "本机缓存") }.compact(11).foregroundStyle(.secondary)
                        if let date = model.cachedAt { Text("缓存于 \(date.formatted(date: .abbreviated, time: .shortened))").compact(11).foregroundStyle(.secondary) }
                        if !model.error.isEmpty { InlineError(message: model.error) { Task { await model.load(app, source) } } }
                        LazyVStack(spacing: 0) {
                            ForEach(model.filtered) { task in
                                NavigationLink(value: task) { TaskRow(task: task).padding(.horizontal, 14).padding(.vertical, 11).contentShape(Rectangle()) }.buttonStyle(.plain).accessibilityIdentifier("task-\(task.id)")
                                if task.id != model.filtered.last?.id { Divider().padding(.leading, 14) }
                            }
                        }.background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20))
                        if model.loading && model.items.isEmpty { ProgressView("加载 Tasks…") }
                        else if model.filtered.isEmpty && model.error.isEmpty { ContentUnavailableView("没有匹配的任务", systemImage: "checklist", description: Text("更换筛选条件，或创建一个 Task。")) }
                    }.padding(.horizontal, 18).padding(.bottom, 20)
                }.background(Color(.systemGroupedBackground)).refreshable { await model.load(app, source) }
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("创建任务", systemImage: "plus") { creating = true }.disabled(!app.hasCredential(source)).accessibilityIdentifier("create-task") } }
        .navigationDestination(for: TaskRecord.self) { TaskDetailView(source: source, task: $0) }
        .navigationDestination(item: $created) { TaskDetailView(source: source, task: $0) }
        .sheet(isPresented: $creating) { NavigationStack { CreateTaskView(source: source) { task in created = task; Task { await model.load(app, source) } } } }
        .onAppear { visible = true }.onDisappear { visible = false }
        .task(id: "\(visible)-\(scene)-\(app.authRevision)-\(app.demoMode)") {
            guard visible, scene == .active, app.hasCredential(source) else { return }
            while !Task.isCancelled { await model.load(app, source); do { try await Task.sleep(for: .seconds(15)) } catch { break } }
        }
    }
}

struct TaskRow: View {
    let task: TaskRecord
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 7) { Text(task.name).compact(13, weight: .semibold).lineLimit(2); Spacer(minLength: 3); StatusBadge(value: task.status) }
            HStack(spacing: 5) { if task.pinned { Image(systemName: "pin.fill").font(.system(size: 9)) }; Text("#\(task.id)").monospacedDigit(); Text("·"); Text(task.description).lineLimit(1) }.compact(11).foregroundStyle(.secondary)
            HStack(spacing: 9) { if task.critical > 0 { FindingCount(label: "Critical", count: task.critical, color: .red) }; if task.high > 0 { FindingCount(label: "High", count: task.high, color: .orange) }; if task.findingsTotal == 0 { Text("暂无发现") }; Spacer(); Text(displayTime(task.updated)).lineLimit(1); Image(systemName: "chevron.right").font(.system(size: 9, weight: .semibold)).foregroundStyle(.tertiary) }.compact(11).foregroundStyle(.secondary)
        }
    }
}

struct FindingCount: View {
    let label: String; let count: Int; let color: Color
    var body: some View { HStack(spacing: 4) { Circle().fill(color).frame(width: 4, height: 4); Text("\(label) \(count)").monospacedDigit() } }
}

struct SearchInput: View {
    let placeholder: String
    @Binding var text: String
    var body: some View { HStack(spacing: 8) { Image(systemName: "magnifyingglass").font(.system(size: 14)).foregroundStyle(.secondary); TextField(placeholder, text: $text).textInputAutocapitalization(.never).autocorrectionDisabled().compact(13); if !text.isEmpty { Button("清除", systemImage: "xmark.circle.fill") { text = "" }.labelStyle(.iconOnly).foregroundStyle(.secondary).frame(minWidth: 44, minHeight: 44) } }.padding(.horizontal, 13).frame(minHeight: 44).background(Color(.tertiarySystemFill).opacity(0.7), in: RoundedRectangle(cornerRadius: 15)) }
}
struct FilterStrip: View {
    @Binding var value: String
    let options: [(String, String)]
    var body: some View { ScrollView(.horizontal) { HStack(spacing: 6) { ForEach(options, id: \.0) { key, label in Button { value = key } label: { Text(label).compact(11, weight: .medium).padding(.horizontal, 12).padding(.vertical, 8).background(value == key ? Color.primary : Color(.tertiarySystemFill).opacity(0.65), in: Capsule()).foregroundStyle(value == key ? Color(.systemBackground) : .secondary).frame(minHeight: 44).contentShape(Rectangle()) }.buttonStyle(.plain).accessibilityAddTraits(value == key ? .isSelected : []) } } }.scrollIndicators(.hidden) }
}

struct CreateTaskView: View {
    let source: RemoteSource
    var initial: TaskDraft? = nil
    var onCreated: (TaskRecord) -> Void
    @Environment(AppStore.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var draft = TaskDraft()
    @State private var busy = false
    @State private var error = ""
    @State private var uncertain = false
    var key: String { source.id.uuidString + ":create-draft" }
    var body: some View {
        Form {
            Section { Text(source.name).compact(13, weight: .semibold); Text(source.address).compact(11).foregroundStyle(.secondary) }
            Section("任务") {
                TextField("任务名称", text: $draft.name).accessibilityIdentifier("task-name")
                TextField("描述（可选）", text: $draft.description, axis: .vertical).lineLimit(2...4)
                TextField("任务目标与约束", text: $draft.goal, axis: .vertical).lineLimit(4...10).accessibilityIdentifier("task-goal")
            }
            Section { DisclosureGroup("高级选项") { TextField("超时分钟数（留空表示无限制）", text: $draft.timeoutMinutes).keyboardType(.numberPad) } } footer: { Text("沿用服务器的模型与执行配置。创建后会立即执行或进入队列。") }
            InlineError(message: error)
            if uncertain { Text("草稿已保留。请返回列表核对是否已经创建，再决定是否重新提交。").compact(12); Button("返回列表核对") { dismiss() } }
        }
        .navigationTitle("创建 Task").navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("保存草稿") { persist(); dismiss() }.disabled(busy) }
            ToolbarItem(placement: .confirmationAction) { Button { submit() } label: { if busy { ProgressView() } else { Text("创建并执行") } }.disabled(!draft.valid || busy || uncertain).accessibilityIdentifier("submit-task") }
        }.interactiveDismissDisabled(busy)
        .onAppear { draft = initial ?? app.local.load(TaskDraft.self, key: key) ?? TaskDraft() }
        .onChange(of: draft) { _, _ in persist() }
    }
    private func persist() { do { try app.local.save(draft, key: key) } catch { self.error = "草稿未能保存：\(error.localizedDescription)" } }
    private func submit() {
        guard draft.valid, !busy, !uncertain else { return }; busy = true; error = ""; let submitted = draft
        Task { defer { busy = false }; do { let raw = try await app.fetch(source, "tasks", method: "POST", body: submitted.payload); let task = try TaskRecord(raw); app.local.remove(key); dismiss(); onCreated(task) } catch { self.error = app.report(error, source: source); uncertain = (error as? APIError) == .uncertain; persist() } }
    }
}

struct TaskDetailView: View {
    let source: RemoteSource
    @State var task: TaskRecord
    @Environment(AppStore.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scene
    @State private var section = "overview"
    @State private var error = ""
    @State private var rename = false
    @State private var newName = ""
    @State private var deleting = false
    @State private var duplicate = false
    @State private var created: TaskRecord?
    @State private var visible = false
    @State private var safeDelete = false
    @State private var deleted = false
    @State private var warning = ""
    var busy: Bool { app.writeBusy(source: source, task: task.id) }
    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 7) {
                HStack { Text(task.name).compact(19, weight: .semibold); Spacer(); StatusBadge(value: task.status) }
                Text("\(source.name) · Task #\(task.id)").compact(11).foregroundStyle(.secondary)
                Picker("内容", selection: $section) { Text("概览").tag("overview"); Text("Sessions").tag("sessions"); Text("Findings").tag("findings") }.pickerStyle(.segmented).accessibilityIdentifier("task-sections")
            }.padding(.horizontal, 16).padding(.vertical, 8)
            if !error.isEmpty { InlineError(message: error) { Task { await refresh() } }.padding(.horizontal, 16) }
            if section == "sessions" { SessionsView(source: source, taskID: task.id, taskName: task.name) }
            else if section == "findings" { FindingsView(source: source, taskID: task.id) }
            else { overview }
        }
        .background(Color(.systemGroupedBackground)).navigationBarTitleDisplayMode(.inline).toolbar(.hidden, for: .tabBar)
        .toolbar { ToolbarItem(placement: .topBarTrailing) { Menu {
            Button(task.pinned ? "取消置顶" : "置顶", systemImage: "pin") { mutate { _ = try await app.fetch(source, "tasks/\(task.id)", method: "PATCH", body: .object(["pinned": .bool(!task.pinned)])) } }
            Button("重命名", systemImage: "pencil") { newName = task.name; rename = true }
            Button("复制基本配置新建", systemImage: "plus.square.on.square") { duplicate = true }
            Button("删除任务并保留证据", systemImage: "trash", role: .destructive) { deleting = true }.disabled(!safeDelete || !(task.canResume || task.terminal))
        } label: { Image(systemName: "ellipsis") }.disabled(busy || deleted) } }
        .alert("重命名 Task", isPresented: $rename) { TextField("名称", text: $newName); Button("保存") { mutate { _ = try await app.fetch(source, "tasks/\(task.id)", method: "PATCH", body: .object(["name": .string(newName)])) } }.disabled(newName.trimmingCharacters(in: .whitespaces).isEmpty || newName.count > 200); Button("取消", role: .cancel) {} }
        .confirmationDialog("删除 \(source.name) 的 \(task.name)？", isPresented: $deleting, titleVisibility: .visible) { Button("删除任务，保留 Findings 和证据", role: .destructive) { deleteTask() } } message: { Text("任务与执行上下文将被删除；独立 Findings、资产、流量、文件和 LLM 记录保留。") }
        .alert("任务已删除", isPresented: $deleted) { Button("返回 Tasks") { dismiss() } } message: { Text(warning.isEmpty ? "独立 Findings 和证据已保留。" : warning) }
        .sheet(isPresented: $duplicate) { NavigationStack { CreateTaskView(source: source, initial: copiedDraft) { created = $0 } } }
        .navigationDestination(item: $created) { TaskDetailView(source: source, task: $0) }
        .onAppear { visible = true }.onDisappear { visible = false }
        .task(id: "\(visible)-\(scene)-\(section)") {
            guard visible, scene == .active else { return }
            do { let health = try await app.fetch(source, "health"); safeDelete = app.demoMode || health["capabilities"]["preserve_delete_v1"].bool } catch {}
            while !Task.isCancelled { await refresh(); do { try await Task.sleep(for: .seconds(5)) } catch { break } }
        }
    }
    var overview: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                if task.canPause || task.canResume {
                    Button { mutate { _ = try await app.fetch(source, "tasks/\(task.id)/control", method: "POST", body: .object(["action": .string(task.canResume ? "resume" : "pause")])) } } label: {
                        HStack(spacing: 8) { Image(systemName: task.canResume ? "play.fill" : "pause.fill"); Text(task.canResume ? "继续任务" : "暂停任务"); Spacer(); Text(task.canResume ? "恢复执行" : "保留当前进度").foregroundStyle(.secondary) }.compact(12, weight: .medium).padding(.horizontal, 14).frame(minHeight: 48)
                    }.buttonStyle(.plain).disabled(busy).background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 16))
                }
                VStack(alignment: .leading, spacing: 10) {
                    Text("执行概况").compact(11, weight: .medium).foregroundStyle(.secondary)
                    HStack(spacing: 0) {
                        metric("已达成目标", value: "\(task.raw["goals_met"].int)", suffix: "/ \(task.raw["goals_total"].int)")
                        Divider().frame(height: 36)
                        metric("运行中 Worker", value: String(task.raw["in_flight"].int))
                        Divider().frame(height: 36)
                        metric("Findings", value: String(task.findingsTotal))
                    }.padding(.vertical, 16).background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 18))
                }
                VStack(alignment: .leading, spacing: 10) {
                    Text("任务目标").compact(11, weight: .medium).foregroundStyle(.secondary)
                    Text(task.goal.isEmpty ? "暂无目标" : task.goal).compact(13).lineSpacing(4).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(16).background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 18))
                }
                VStack(spacing: 12) {
                    LabeledContent("输入 / 输出 Token", value: "\(task.raw["tokens"]["input_tokens"].int.formatted()) / \(task.raw["tokens"]["output_tokens"].int.formatted())")
                    Divider()
                    LabeledContent("创建时间", value: displayTime(task.raw["created_at"].string))
                    LabeledContent("最近活动", value: displayTime(task.updated))
                }.compact(12).padding(16).background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 18))
                if !task.raw["llm_failover_reason"].string.isEmpty { InlineError(message: task.raw["llm_failover_reason"].string) }
                if !safeDelete { Text("此服务器暂不支持保留证据的删除操作。").compact(11).foregroundStyle(.secondary).padding(.horizontal, 2) }
            }.padding(16)
        }.refreshable { await refresh() }
    }
    private func metric(_ title: String, value: String, suffix: String = "") -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 4) { Text(value).compact(23, weight: .semibold).monospacedDigit(); if !suffix.isEmpty { Text(suffix).compact(11).foregroundStyle(.secondary) } }
            Text(title).compact(10).foregroundStyle(.secondary)
        }.frame(maxWidth: .infinity, alignment: .leading).padding(.leading, 16)
    }
    var copiedDraft: TaskDraft { var d = TaskDraft(); d.name = String((task.name + " 副本").prefix(200)); d.description = task.description; d.goal = task.goal; return d }
    func refresh() async { guard !deleted else { return }; do { let raw = try await app.fetch(source, "tasks/\(task.id)"); try Task.checkCancellation(); task = try TaskRecord(raw); if error != APIError.uncertain.localizedDescription { error = "" } } catch is CancellationError {} catch { self.error = app.report(error, source: source) } }
    func mutate(_ operation: @escaping @MainActor () async throws -> Void) { guard app.beginWrite(source: source, task: task.id) else { return }; Task { defer { app.endWrite(source: source, task: task.id) }; do { try await operation(); error = "" } catch { self.error = app.report(error, source: source) }; await refresh() } }
    func deleteTask() { mutate { let result = try await app.fetch(source, "tasks/\(task.id)", method: "DELETE", body: .object(["delete_assets": .bool(false), "delete_traffic": .bool(false), "delete_files": .bool(false), "delete_findings": .bool(false), "delete_llm_records": .bool(false)])); warning = result["cleanup_warning"].string; deleted = true } }
}
