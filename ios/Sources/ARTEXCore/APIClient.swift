import Foundation

public enum APIError: Error, LocalizedError, Equatable, Sendable {
    case invalidAddress, invalidResponse, unauthenticated, http(Int, String), uncertain, network(String)
    public var errorDescription: String? {
        switch self {
        case .invalidAddress: return "请输入有效的 HTTPS 地址，不包含账号、密码、查询参数或片段。"
        case .invalidResponse: return "服务器响应不是预期的 ARTEX 数据，请检查地址或访问网关。"
        case .unauthenticated: return "此远程源需要登录，其他源不受影响。"
        case .http(let code, let reason): return reason.isEmpty ? "服务器返回 HTTP \(code)" : reason
        case .uncertain: return "提交结果待确认。请先刷新并核对服务器记录，不要重复提交。"
        case .network(let text): return text
        }
    }
}

public struct ServerEndpoint: Hashable, Sendable {
    public let baseURL: URL
    public init(_ input: String, allowLoopback: Bool = false) throws {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let c = URLComponents(string: text), let host = c.host, !host.isEmpty,
              c.user == nil, c.password == nil, c.query == nil, c.fragment == nil,
              c.scheme == "https" || (allowLoopback && c.scheme == "http" && ["127.0.0.1", "localhost", "::1"].contains(host)),
              let url = c.url else { throw APIError.invalidAddress }
        baseURL = url
    }
    public func url(_ path: String, query: [String: String] = [:]) -> URL {
        let url = path.split(separator: "/").reduce(baseURL.appendingPathComponent("api")) { $0.appendingPathComponent(String($1)) }
        var parts = URLComponents(url: url, resolvingAgainstBaseURL: false)!
        if !query.isEmpty { parts.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) } }
        return parts.url!
    }
}

private final class NoRedirect: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}

public final class APIClient: @unchecked Sendable {
    public let endpoint: ServerEndpoint
    private let token: String?
    private let session: URLSession
    public init(endpoint: ServerEndpoint, token: String? = nil, session: URLSession? = nil) {
        self.endpoint = endpoint; self.token = token
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        config.timeoutIntervalForResource = 3600
        config.httpCookieStorage = nil; config.urlCache = nil
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        self.session = session ?? URLSession(configuration: config, delegate: NoRedirect(), delegateQueue: nil)
    }
    public func request(_ path: String, method: String = "GET", query: [String: String] = [:], body: JSONValue? = nil) throws -> URLRequest {
        var r = URLRequest(url: endpoint.url(path, query: query))
        r.httpMethod = method; r.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token { r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body { r.httpBody = try JSONEncoder().encode(body); r.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        return r
    }
    public func call(_ path: String, method: String = "GET", query: [String: String] = [:], body: JSONValue? = nil) async throws -> JSONValue {
        let r = try request(path, method: method, query: query, body: body)
        let data: Data; let response: URLResponse
        do { (data, response) = try await session.data(for: r) }
        catch {
            if Task.isCancelled { throw CancellationError() }
            if method != "GET" { throw APIError.uncertain }
            throw APIError.network(Self.networkMessage(error))
        }
        guard let http = response as? HTTPURLResponse else { throw APIError.invalidResponse }
        if http.statusCode == 401 { throw APIError.unauthenticated }
        let json = (try? JSONDecoder().decode(JSONValue.self, from: data)) ?? .null
        guard (200..<300).contains(http.statusCode) else { throw APIError.http(http.statusCode, json["error"].string) }
        if http.statusCode == 204 { return .object([:]) }
        guard json != .null else { if method != "GET" { throw APIError.uncertain }; throw APIError.invalidResponse }
        return json
    }
    public func health() async throws -> JSONValue {
        let r = try await call("health")
        guard r["ok"].bool, r["service"].string == "artex" else { throw APIError.invalidResponse }
        return r
    }
    public func tasks() async throws -> [TaskRecord] { try await call("tasks")["tasks"].array.map(TaskRecord.init) }
    public func task(_ id: String) async throws -> TaskRecord { try TaskRecord(await call("tasks/\(id)")) }
    public func createTask(_ draft: TaskDraft) async throws -> TaskRecord { try TaskRecord(await call("tasks", method: "POST", body: draft.payload)) }
    public func patchTask(_ id: String, fields: [String: JSONValue]) async throws -> TaskRecord { try TaskRecord(await call("tasks/\(id)", method: "PATCH", body: .object(fields))) }
    public func controlTask(_ id: String, action: String) async throws { _ = try await call("tasks/\(id)/control", method: "POST", body: .object(["action": .string(action)])) }
    public func deleteTask(_ id: String) async throws -> JSONValue { try await call("tasks/\(id)", method: "DELETE", body: .object(["delete_assets": .bool(false), "delete_traffic": .bool(false), "delete_files": .bool(false), "delete_findings": .bool(false), "delete_llm_records": .bool(false)])) }
    public func findings(page: Int, query: String = "", severity: String = "", status: String = "", task: String = "", sort: String = "severity") async throws -> JSONValue {
        try await call("exploration/findings", query: ["page": String(page), "limit": "40", "q": query, "severity": severity, "status": status, "task_id": task, "sort": sort])
    }
    public func history(task: String, session: String, before: Int = 0) async throws -> JSONValue { try await call("exploration/activity/history", query: ["task": task, "session": session, "before": String(before), "limit": "60"]) }
    public func activityDetail(task: String, seq: Int) async throws -> String { try await call("exploration/activity/\(seq)", query: ["task": task])["detail"].string }
    public func chatStatus(task: String) async throws -> Bool { try await call("tasks/\(task)/chat/status")["running"].bool }
    public func send(task: String, segment: Int, message: String) async throws -> JSONValue { try await call("chat", method: "POST", query: ["task": task], body: .object(["message": .string(message), "seg": .integer(Int64(segment))])) }

    public func stream(task: String, since: Int, receive: @escaping @Sendable (ActivityRecord) async -> Void) async throws {
        var req = try request("exploration/activity/stream", query: ["task": task, "since": String(since)])
        req.timeoutInterval = 60
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        let (bytes, response) = try await session.bytes(for: req)
        guard let response = response as? HTTPURLResponse else { throw APIError.invalidResponse }
        if response.statusCode == 401 { throw APIError.unauthenticated }
        guard response.statusCode == 200, response.value(forHTTPHeaderField: "Content-Type")?.contains("text/event-stream") == true else { throw APIError.http(response.statusCode, "实时活动连接不可用") }
        var parser = SSEParser()
        for try await line in bytes.lines {
            try Task.checkCancellation()
            if let payload = parser.feed(line), let record = try? ActivityRecord(JSONValue.parse(payload)) { await receive(record) }
        }
    }
    private static func networkMessage(_ error: Error) -> String {
        switch (error as? URLError)?.code {
        case .cannotFindHost, .dnsLookupFailed: return "无法解析服务器地址，请检查 Tailscale 连接和域名。"
        case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateHasUnknownRoot, .secureConnectionFailed: return "HTTPS 证书验证失败，未向无效连接发送凭据。"
        case .timedOut: return "连接超时，请检查 Tailscale、服务地址和访问策略。"
        case .notConnectedToInternet, .cannotConnectToHost, .networkConnectionLost: return "服务器暂时不可达，请检查网络和 Tailscale。"
        default: return error.localizedDescription
        }
    }
}

public struct SSEParser: Sendable {
    private var data: [String] = []
    public init() {}
    public mutating func feed(_ line: String) -> String? {
        if line.isEmpty { defer { data.removeAll(keepingCapacity: true) }; return data.isEmpty ? nil : data.joined(separator: "\n") }
        if line.hasPrefix("data:") { var payload = String(line.dropFirst(5)); if payload.first == " " { payload.removeFirst() }; data.append(payload) }
        return nil
    }
}
