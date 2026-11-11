import XCTest
@testable import ARTEXCore

final class ARTEXCoreTests: XCTestCase {
    func testEndpointDoesNotLeakCredentialsAndPreservesBasePath() throws {
        for bad in ["http://example.com", "https://user:pass@example.com", "https://example.com/?token=secret", "https://example.com/#fragment", "file:///tmp/a"] { XCTAssertThrowsError(try ServerEndpoint(bad), bad) }
        let endpoint = try ServerEndpoint("https://host.example/artex/")
        let url = endpoint.url("exploration/findings", query: ["task_id":"52", "q":"a&b 中文"])
        XCTAssertEqual(url.path, "/artex/api/exploration/findings")
        XCTAssertEqual(URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "q" }?.value, "a&b 中文")
        let req = try APIClient(endpoint: endpoint, token: "secret").request("tasks")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer secret")
        XCTAssertFalse(req.url!.absoluteString.contains("secret"))
    }
    func testNullArraysAndUnknownTaskStatus() throws {
        let result = JSONValue.parse(#"{"tasks":null}"#)
        XCTAssertTrue(result["tasks"].array.isEmpty)
        let task = try TaskRecord(.parse(#"{"id":52,"status":"new-server-state","description":"标题"}"#))
        XCTAssertEqual(task.id, "52"); XCTAssertEqual(task.name, "标题")
        XCTAssertFalse(task.canPause); XCTAssertFalse(task.canResume)
        XCTAssertThrowsError(try TaskRecord(.parse(#"{"status":"running"}"#)))
    }
    func testMainSegmentRoutingAndInheritedIdentity() throws {
        let main = try ActivityRecord(.parse(#"{"seq":4,"worker":"mainagent","main_seg":2,"kind":"text","summary":"hello"}"#))
        let other = try ActivityRecord(.parse(#"{"seq":4,"worker":"work#1","intent_id":"17","source_task_id":"99","kind":"text"}"#))
        XCTAssertEqual(main.sessionKey, "main:2"); XCTAssertEqual(other.sessionKey, "intent:17")
        XCTAssertEqual(ActivityRecord.merge([main], [main, other]).count, 2)
        let system = try ActivityRecord(.parse(#"{"seq":5,"worker":"mainagent","kind":"llm_switch"}"#))
        XCTAssertEqual(system.sessionKey, "system")
        XCTAssertEqual(SessionDescriptor.main(0).segment, 0)
    }
    func testSSEMultilineHeartbeatsAndDedup() throws {
        var parser = SSEParser()
        XCTAssertNil(parser.feed(": heartbeat")); XCTAssertNil(parser.feed(""))
        XCTAssertNil(parser.feed("id: 8")); XCTAssertNil(parser.feed("data: {\"seq\":8,")); XCTAssertNil(parser.feed("data: \"worker\":\"planner\"}"))
        let event = try ActivityRecord(.parse(try XCTUnwrap(parser.feed(""))))
        XCTAssertEqual(event.seq, 8); XCTAssertEqual(event.sessionKey, "plan")
        XCTAssertNil(parser.feed(""))
    }
    func testCreateValidationAndTaskScope() throws {
        var draft = TaskDraft(); XCTAssertFalse(draft.valid)
        draft.name = "检查"; draft.goal = "明确范围"; draft.timeoutMinutes = "-5"; XCTAssertFalse(draft.valid)
        draft.timeoutMinutes = "10"; XCTAssertTrue(draft.valid)
        XCTAssertEqual(draft.payload["timeout_seconds"].int, 600)
        XCTAssertEqual(draft.payload["description"].string, "检查")
        let client = APIClient(endpoint: try ServerEndpoint("https://host.example"))
        let request = try client.request("chat", method: "POST", query: ["task":"52"], body: .object(["message":.string("hint"),"seg":.integer(2)]))
        XCTAssertEqual(URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems?.first?.value, "52")
        XCTAssertEqual(JSONValue.parse(String(decoding: request.httpBody!, as: UTF8.self))["seg"].int, 2)
    }
    func testHTTPConflictAndUncertainWriteAreNotRetried() async throws {
        let configuration = URLSessionConfiguration.ephemeral; configuration.protocolClasses = [FixtureProtocol.self]
        let client = APIClient(endpoint: try ServerEndpoint("https://fixture.example"), token: "private", session: URLSession(configuration: configuration))
        FixtureProtocol.reset(mode: .conflict)
        do { _ = try await client.send(task: "52", segment: 2, message: "hint"); XCTFail("expected conflict") } catch let error as APIError { XCTAssertEqual(error, .http(409, "busy")) }
        XCTAssertEqual(FixtureProtocol.count, 1)
        FixtureProtocol.reset(mode: .timeout)
        do { _ = try await client.send(task: "52", segment: 2, message: "hint"); XCTFail("expected uncertain result") } catch let error as APIError { XCTAssertEqual(error, .uncertain) }
        XCTAssertEqual(FixtureProtocol.count, 1)
        FixtureProtocol.reset(mode: .accepted)
        let accepted = try await client.send(task: "52", segment: 2, message: "hint")
        XCTAssertEqual(accepted["status"].string, "accepted")
    }
}

final class FixtureProtocol: URLProtocol, @unchecked Sendable {
    enum Mode { case conflict, timeout, accepted }
    private static let lock = NSLock()
    private static var mode = Mode.accepted
    private static var calls = 0
    static var count: Int { lock.lock(); defer { lock.unlock() }; return calls }
    static func reset(mode: Mode) { lock.lock(); defer { lock.unlock() }; self.mode = mode; calls = 0 }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lock.lock(); Self.calls += 1; let mode = Self.mode; Self.lock.unlock()
        if mode == .timeout { client?.urlProtocol(self, didFailWithError: URLError(.timedOut)); return }
        let status = mode == .conflict ? 409 : 202
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type":"application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((mode == .conflict ? #"{"error":"busy"}"# : #"{"status":"accepted","mode":"llm"}"#).utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
