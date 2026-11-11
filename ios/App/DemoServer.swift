import Foundation

@MainActor final class DemoServer {
    var tasks: [JSONValue] = [
        .parse(##"{"id":"52","name":"商城 API 安全评估","description":"shop.example.test · API 与访问控制","goal":"检查测试商城的授权边界，整理已有证据并避免重复验证。","status":"running","pinned":true,"created_at":"2026-11-11T01:12:00Z","last_activity":"2026-11-11T02:40:00Z","in_flight":2,"goals_met":3,"goals_total":8,"findings":{"critical":1,"high":2},"tokens":{"input_tokens":128400,"output_tokens":42800}}"##),
        .parse(##"{"id":"51","name":"登录与会话安全检查","description":"auth.example.test · 登录与会话","goal":"验证会话管理。","status":"queued","created_at":"2026-11-11T01:09:00Z","findings":{}}"##),
        .parse(##"{"id":"49","name":"管理后台权限复核","description":"admin.example.test · 角色权限","goal":"复核角色权限边界。","status":"paused","created_at":"2026-11-10T03:00:00Z","findings":{"high":1}}"##),
        .parse(##"{"id":"47","name":"文件服务安全基线","description":"files.example.test · 存储与下载","goal":"检查文件服务基线。","status":"done","created_at":"2026-11-10T01:00:00Z","findings":{}}"##)
    ]
    var findings: [JSONValue] = [
        .parse(##"{"id":"108","finding_id":"108","task_id":"52","name":"订单接口缺少对象级访问校验","severity":"high","status":"confirmed","vulnclass":"访问控制","summary":"测试环境的订单接口未验证资源与当前用户的归属关系。","evidence":"已保存两组测试身份的响应对照。","report":"# 订单对象级访问控制\n\n## 影响\n测试身份可读取权限范围外的订单。\n\n## 修复建议\n在服务端统一验证资源归属，并加入回归检查。","assets":[{"id":"7","label":"shop.example.test","type":"domain"}],"ts":"2026-11-11T02:30:00Z","traffic_count":2}"##),
        .parse(##"{"id":"107","task_id":"52","name":"内部配置文件可被公开读取","severity":"critical","status":"confirmed","vulnclass":"信息暴露","summary":"内部配置不应向外公开。","evidence":"隔离环境验证。","ts":"2026-11-11T02:25:00Z"}"##)
    ]
    var messages: [JSONValue] = [
        .parse(##"{"seq":101,"worker":"mainagent","main_seg":2,"kind":"user","summary":"优先关注订单接口的权限边界，已有发现不要重复验证。","ts":"2026-11-11T02:28:00Z"}"##),
        .parse(##"{"seq":102,"worker":"mainagent","main_seg":2,"kind":"text","summary":"收到。我会将这个方向补充到任务提示中，并结合当前执行结果安排后续检查。","ts":"2026-11-11T02:28:10Z"}"##),
        .parse(##"{"seq":103,"worker":"mainagent","main_seg":2,"kind":"tool_result","tool":"add_hint","summary":"策略提示已记录","detail":"演示模式：此操作不会发送到服务器。","ts":"2026-11-11T02:28:11Z"}"##)
    ]
    func call(_ path: String, method: String, query: [String: String], body: JSONValue?) throws -> JSONValue {
        if path == "health" { return .parse(##"{"ok":true,"service":"artex","version":"demo"}"##) }
        if path == "tasks" {
            if method == "POST", case .object(var fields) = body { fields["id"] = .string(String((tasks.compactMap { Int($0["id"].string) }.max() ?? 52) + 1)); fields["status"] = .string("queued"); fields["created_at"] = .string(ISO8601DateFormatter().string(from: Date())); let result = JSONValue.object(fields); tasks.insert(result, at: 0); return result }
            return .object(["tasks": .array(tasks)])
        }
        if path.hasPrefix("tasks/") {
            let id = String(path.split(separator: "/")[1])
            if path.hasSuffix("chat/status") { return .object(["running": .bool(false)]) }
            if path.hasSuffix("chat/stop") { return .object(["status": .string("idle")]) }
            guard let i = tasks.firstIndex(where: { $0["id"].string == id }) else { throw APIError.http(404, "Task 已不存在") }
            if method == "DELETE" { tasks.remove(at: i); return .object(["deleted": .string(id)]) }
            if case .object(var fields) = tasks[i] {
                if path.hasSuffix("control") { fields["status"] = .string(body?["action"].string == "pause" ? "paused" : "running") }
                if method == "PATCH", case .object(let patch) = body { patch.forEach { fields[$0.key] = $0.value } }
                tasks[i] = .object(fields)
            }
            return tasks[i]
        }
        if path == "exploration/findings" {
            let filtered = findings.filter { f in (query["task_id", default: ""].isEmpty || f["task_id"].string == query["task_id"]) && (query["severity", default: ""].isEmpty || f["severity"].string == query["severity"]) && (query["status", default: ""].isEmpty || f["status"].string == query["status"]) && (query["q", default: ""].isEmpty || f.pretty.localizedCaseInsensitiveContains(query["q"]!)) }
            return .object(["items": .array(filtered), "total": .integer(Int64(filtered.count))])
        }
        if path.hasPrefix("exploration/findings/") { return findings.first { $0["id"].string == path.split(separator: "/").last.map(String.init) } ?? .null }
        if path == "exploration/main-sessions" { return .parse(##"{"sessions":[{"seq":2},{"seq":1},{"seq":0}],"current":2}"##) }
        if path == "exploration/intents" {
            let before = Int(query["before"] ?? "0") ?? 0
            let ids = (1...38).reversed().filter { before == 0 || $0 < before }.prefix(20)
            let items = ids.map { n in JSONValue.object(["id": .string(String(n)), "payload": .string("{\"summary\":\"\(n > 35 ? "订单访问控制检查" : "接口响应检查") #\(n)\"}"), "state": .string(n > 35 ? "claimed" : "done"), "ts": .string("2026-11-11T02:28:00Z")]) }
            return .object(["items": .array(items), "has_more": .bool((ids.last ?? 1) > 1)])
        }
        if path == "exploration/activity/history" {
            let key = query["session"] ?? "main:2"
            let items = key == "main:2" ? messages : [.object(["seq": .integer(88), "worker": .string(key == "plan" ? "planner" : "work#1"), "intent_id": .string(String(key.split(separator: ":").last ?? "1")), "kind": .string("tool_result"), "summary": .string("执行记录已保存，可展开查看输出。"), "tool": .string("inspect"), "detail": .string("演示内容：已检查接口响应及权限约束。"), "ts": .string("2026-11-11T02:28:00Z")])]
            return .object(["items": .array(items), "snapshot_cursor": .integer(103), "earliest_cursor": .integer(88), "has_more": .bool(false)])
        }
        if path.hasPrefix("exploration/activity/") { return .object(["detail": .string("演示输出：记录已保存，无真实服务器操作。")]) }
        if path == "chat" {
            let seq = (messages.last?["seq"].int ?? 103) + 1
            for (offset, kind, text) in [(0, "user", body?["message"].string ?? ""), (1, "text", "已收到这条演示消息。真实连接时，此处显示主 Agent 的回复。") ] {
                messages.append(.object(["seq": .integer(Int64(seq + offset)), "worker": .string("mainagent"), "main_seg": body?["seg"] ?? .integer(2), "kind": .string(kind), "summary": .string(text), "ts": .string(ISO8601DateFormatter().string(from: Date()))]))
            }
            return .object(["mode": .string("rule"), "reply": .string("演示回复已生成")])
        }
        throw APIError.http(404, "演示模式未提供此接口")
    }
}
