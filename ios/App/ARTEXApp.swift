import SwiftUI

@main struct ARTEXApp: App {
    @State private var store = AppStore()
    var body: some Scene { WindowGroup { RootView().environment(store).tint(.blue) } }
}

struct RootView: View {
    @Environment(AppStore.self) private var store
    @State private var tab = 0
    var body: some View {
        TabView(selection: $tab) {
            Tab("Tasks", systemImage: "checklist", value: 0) { NavigationStack { TasksView() }.id(store.selectedID) }
            Tab("Findings", systemImage: "checkmark.shield", value: 1) { NavigationStack { FindingsView(source: store.selected, taskID: nil) }.id(store.selectedID) }
        }
        .alert("提示", isPresented: Binding(get: { store.error != nil }, set: { if !$0 { store.error = nil } })) { Button("好") { store.error = nil } } message: { Text(store.error ?? "") }
        .task { await store.consumeDeviceEnrollment() }
    }
}

struct CompactText: ViewModifier {
    @ScaledMetric(relativeTo: .body) var size: CGFloat = 13
    var weight: Font.Weight = .regular
    func body(content: Content) -> some View { content.font(.system(size: size, weight: weight)) }
}
extension View { func compact(_ size: CGFloat = 13, weight: Font.Weight = .regular) -> some View { modifier(CompactText(size: size, weight: weight)) } }

struct StatusBadge: View {
    let value: String
    var color: Color { switch value { case "critical", "high", "failed": .red; case "running", "claimed", "confirmed", "done", "completed": .green; case "queued", "timeout", "medium": .orange; case "paused": .blue; default: .secondary } }
    var body: some View { Text(StatusLabel.text(value)).compact(11, weight: .medium).foregroundStyle(color).padding(.horizontal, 6).padding(.vertical, 3).background(color.opacity(0.10), in: RoundedRectangle(cornerRadius: 5)) }
}

struct InlineError: View {
    let message: String
    var retry: (() -> Void)? = nil
    var body: some View { if !message.isEmpty { VStack(alignment: .leading, spacing: 8) { Label(message, systemImage: "exclamationmark.circle").compact(12).foregroundStyle(.orange); if let retry { Button("重试", action: retry).frame(minHeight: 44) } }.padding(.vertical, 6).textSelection(.enabled) } }
}

func displayTime(_ text: String) -> String {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let date = f.date(from: text) ?? ISO8601DateFormatter().date(from: text)
    guard let date else { return text.isEmpty ? "暂无时间" : text }
    return date.formatted(.relative(presentation: .named))
}
