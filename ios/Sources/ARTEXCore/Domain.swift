import Foundation

public enum JSONValue: Codable, Hashable, Sendable {
    case object([String: JSONValue]), array([JSONValue]), string(String), integer(Int64), number(Double), bool(Bool), null
    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let v = try? c.decode(Bool.self) { self = .bool(v) }
        else if let v = try? c.decode(Int64.self) { self = .integer(v) }
        else if let v = try? c.decode(Double.self) { self = .number(v) }
        else if let v = try? c.decode(String.self) { self = .string(v) }
        else if let v = try? c.decode([JSONValue].self) { self = .array(v) }
        else { self = .object(try c.decode([String: JSONValue].self)) }
    }
    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .object(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .string(let v): try c.encode(v)
        case .integer(let v): try c.encode(v)
        case .number(let v): try c.encode(v)
        case .bool(let v): try c.encode(v)
        case .null: try c.encodeNil()
        }
    }
    public subscript(_ key: String) -> JSONValue { if case .object(let v) = self { return v[key] ?? .null }; return .null }
    public var string: String { switch self { case .string(let v): return v; case .integer(let v): return String(v); default: return "" } }
    public var int: Int { switch self { case .integer(let v): return Int(v); case .number(let v): return Int(v); case .string(let v): return Int(v) ?? 0; default: return 0 } }
    public var bool: Bool { if case .bool(let v) = self { return v }; return false }
    public var array: [JSONValue] { if case .array(let v) = self { return v }; return [] }
    public static func parse(_ text: String) -> JSONValue { (try? JSONDecoder().decode(Self.self, from: Data(text.utf8))) ?? .null }
    public var pretty: String { let e = JSONEncoder(); e.outputFormatting = [.prettyPrinted, .sortedKeys]; return (try? String(decoding: e.encode(self), as: UTF8.self)) ?? string }
}

public struct RemoteSource: Identifiable, Codable, Hashable, Sendable {
    public var id: UUID
    public var name: String
    public var address: String
    public var note: String
    public var version: String?
    public var verifiedAt: Date?
    public init(id: UUID = UUID(), name: String, address: String, note: String = "通过 Tailscale 访问") { self.id = id; self.name = name; self.address = address; self.note = note }
}

public struct TaskRecord: Identifiable, Codable, Hashable, Sendable {
    public let raw: JSONValue
    public init(_ raw: JSONValue) throws { guard !raw["id"].string.isEmpty else { throw APIError.invalidResponse }; self.raw = raw }
    public var id: String { raw["id"].string }
    public var name: String { raw["name"].string.isEmpty ? (raw["description"].string.isEmpty ? "Task #\(id)" : raw["description"].string) : raw["name"].string }
    public var description: String { raw["description"].string }
    public var goal: String { raw["goal"].string }
    public var status: String { raw["status"].string }
    public var pinned: Bool { raw["pinned"].bool }
    public var terminal: Bool { ["done", "failed", "timeout"].contains(status) }
    public var canPause: Bool { ["created", "queued", "running"].contains(status) }
    public var canResume: Bool { status == "paused" }
    public var updated: String { raw["last_activity"].string.isEmpty ? raw["created_at"].string : raw["last_activity"].string }
    public var critical: Int { raw["findings"]["critical"].int }
    public var high: Int { raw["findings"]["high"].int }
    public var findingsTotal: Int { ["critical", "high", "medium", "low"].reduce(0) { $0 + raw["findings"][$1].int } }
}

public struct FindingRecord: Identifiable, Codable, Hashable, Sendable {
    public let raw: JSONValue
    public init(_ raw: JSONValue) throws { guard !raw["id"].string.isEmpty else { throw APIError.invalidResponse }; self.raw = raw }
    public var id: String { raw["finding_id"].string.isEmpty ? raw["id"].string : raw["finding_id"].string }
    public var name: String { raw["name"].string.isEmpty ? raw["vulnclass"].string : raw["name"].string }
    public var severity: String { raw["severity"].string }
    public var status: String { raw["status"].string }
    public var taskID: String { raw["task_id"].string }
    public var assetText: String { raw["assets"].array.map { $0["label"].string }.joined(separator: " · ") }
}

public struct SessionDescriptor: Identifiable, Hashable, Sendable {
    public let id: String
    public let title: String
    public let role: String
    public let status: String
    public let timestamp: String
    public let inherited: Bool
    public var segment: Int? { id.hasPrefix("main:") ? Int(id.dropFirst(5)) : nil }
    public var writable: Bool { segment != nil && !inherited }
    public init(id: String, title: String, role: String, status: String = "", timestamp: String = "", inherited: Bool = false) { self.id = id; self.title = title; self.role = role; self.status = status; self.timestamp = timestamp; self.inherited = inherited }
    public static func main(_ segment: Int) -> Self { .init(id: "main:\(segment)", title: "主对话 #\(segment)", role: "Main Agent") }
    public static func worker(_ raw: JSONValue) -> Self {
        let payload = JSONValue.parse(raw["payload"].string)
        let label = payload["summary"].string
        return .init(id: "intent:\(raw["id"].string)", title: label.isEmpty ? "Worker #\(raw["id"].string)" : label, role: "Worker #\(raw["id"].string)", status: raw["state"].string, timestamp: raw["ts"].string, inherited: raw["inherited"].bool)
    }
}

public struct ActivityRecord: Identifiable, Codable, Hashable, Sendable {
    public let raw: JSONValue
    public init(_ raw: JSONValue) throws { guard raw["seq"].int > 0 else { throw APIError.invalidResponse }; self.raw = raw }
    public var id: String { "\(raw["source_task_id"].string):\(seq)" }
    public var seq: Int { raw["seq"].int }
    public var kind: String { raw["kind"].string }
    public var summary: String { raw["summary"].string }
    public var detail: String { raw["detail"].string }
    public var sessionKey: String {
        let worker = raw["worker"].string
        if worker == "system" || ["llm_switch", "llm_failover"].contains(kind) { return "system" }
        if worker == "mainagent" { return "main:\(raw["main_seg"].int)" }
        if worker == "planner" { return "plan" }
        if !raw["intent_id"].string.isEmpty { return "intent:\(raw["intent_id"].string)" }
        return "unknown"
    }
    public static func merge(_ old: [Self], _ new: [Self]) -> [Self] {
        var map = Dictionary(old.map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
        for record in new { map[record.id] = record }
        return map.values.sorted { $0.seq < $1.seq }
    }
}

public struct TaskDraft: Codable, Equatable, Sendable {
    public var name = ""
    public var description = ""
    public var goal = ""
    public var timeoutMinutes = ""
    public init() {}
    public var valid: Bool { !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && name.count <= 200 && !goal.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && (timeoutMinutes.isEmpty || (Int(timeoutMinutes).map { $0 > 0 && $0 <= 525600 } ?? false)) }
    public var payload: JSONValue { .object(["name": .string(name.trimmingCharacters(in: .whitespacesAndNewlines)), "description": .string(description.isEmpty ? name : description), "goal": .string(goal), "timeout_seconds": .integer(Int64((Int(timeoutMinutes) ?? 0) * 60)), "coverage_enabled": .bool(true)]) }
}

public enum StatusLabel {
    public static func text(_ value: String) -> String { ["created":"已创建", "queued":"排队中", "running":"运行中", "paused":"已暂停", "done":"已完成", "failed":"失败", "timeout":"超时", "pending":"待处理", "in_progress":"处理中", "confirmed":"已确认", "resolved":"已解决", "fixed":"已修复", "false_positive":"误报", "ignored":"已忽略", "duplicate":"重复", "risk_accepted":"已接受风险", "ready":"就绪", "claimed":"执行中", "completed":"已完成", "stopped":"已停止", "deleted":"已删除"][value] ?? value }
}
