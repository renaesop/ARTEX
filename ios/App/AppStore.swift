import Foundation
import Observation
import SwiftUI

@MainActor @Observable final class AppStore {
    var sources: [RemoteSource]
    var selectedID: UUID?
    var sourceStates: [UUID: String] = [:]
    var error: String?
    var authRevision = 0
    var demoMode = false
    let local = LocalStore()
    let demo = DemoServer()
    private var authenticated = Set<UUID>()
    private var writes = Set<String>()
    private var enrollmentChecked = false
    var selected: RemoteSource? { sources.first { $0.id == selectedID } }
    init() {
        sources = local.load([RemoteSource].self, key: "sources") ?? []
        selectedID = local.load(UUID.self, key: "selected-source") ?? sources.first?.id
        if !sources.contains(where: { $0.id == selectedID }) { selectedID = sources.first?.id }
        for source in sources { if let _ = try? CredentialVault.read(source.id) { authenticated.insert(source.id); sourceStates[source.id] = "待验证" } else { sourceStates[source.id] = "需要登录" } }
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("--demo") { setDemoMode(true) }
        #endif
    }
    #if DEBUG
    func setDemoMode(_ enabled: Bool) {
        demoMode = enabled
        if enabled {
            sources = [RemoteSource(id: UUID(uuidString: "00000000-0000-4000-8000-000000000001")!, name: "演示 ARTEX", address: "https://demo.example.invalid")]
            selectedID = sources.first?.id
        } else {
            sources = local.load([RemoteSource].self, key: "sources") ?? []
            selectedID = local.load(UUID.self, key: "selected-source") ?? sources.first?.id
            if !sources.contains(where: { $0.id == selectedID }) { selectedID = sources.first?.id }
        }
        authRevision += 1
    }
    #endif
    func hasCredential(_ source: RemoteSource) -> Bool { demoMode || authenticated.contains(source.id) }
    // Debug deployment only: the paired Mac transfers a one-use file into this
    // app's private, non-backed-up Caches directory. It is consumed into Keychain
    // and removed before any network request. No URL scheme or clipboard import.
    func consumeDeviceEnrollment() async {
        #if DEBUG
        guard !demoMode, !enrollmentChecked else { return }
        enrollmentChecked = true
        let cache = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        let file = cache.appendingPathComponent("ARTEXDeviceEnrollment.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return }
        struct Entry: Decodable { var sourceID: UUID; var address: String; var credential: Credential }
        struct Receipt: Encodable { var sourceID: UUID; var stored: Bool; var connected: Bool; var message: String }
        var receipts: [Receipt] = []
        var imported: [RemoteSource] = []
        do {
            try FileManager.default.setAttributes([.protectionKey: FileProtectionType.complete], ofItemAtPath: file.path)
            let data = try Data(contentsOf: file)
            guard data.count <= 32768 else { throw APIError.invalidResponse }
            let entries = try JSONDecoder().decode([Entry].self, from: data)
            guard entries.count <= sources.count else { throw APIError.invalidResponse }
            for entry in entries {
                guard let source = sources.first(where: { $0.id == entry.sourceID && $0.address == entry.address }),
                      entry.credential.kind == "app", entry.credential.token.hasPrefix("artex_app_"),
                      entry.credential.token.count == 53 else { throw APIError.invalidResponse }
                try CredentialVault.save(entry.credential, id: source.id)
                authenticated.insert(source.id); sourceStates[source.id] = "待验证"
                imported.append(source)
            }
        } catch { self.error = "设备凭据导入失败，请重新登录。" }
        do { try FileManager.default.removeItem(at: file) } catch { self.error = "设备凭据临时文件清理失败。" }
        authRevision += 1
        for source in imported {
            do { try await check(source); receipts.append(Receipt(sourceID: source.id, stored: true, connected: true, message: "verified")) }
            catch { receipts.append(Receipt(sourceID: source.id, stored: true, connected: false, message: report(error, source: source))) }
        }
        if let data = try? JSONEncoder().encode(receipts) { try? data.write(to: cache.appendingPathComponent("ARTEXEnrollmentReceipt.json"), options: [.atomic, .completeFileProtection]) }
        #endif
    }
    func fetch(_ source: RemoteSource, _ path: String, method: String = "GET", query: [String: String] = [:], body: JSONValue? = nil) async throws -> JSONValue {
        if demoMode { return try demo.call(path, method: method, query: query, body: body) }
        return try await client(source).call(path, method: method, query: query, body: body)
    }
    func client(_ source: RemoteSource, token: String? = nil) throws -> APIClient { try APIClient(endpoint: ServerEndpoint(source.address), token: token ?? CredentialVault.read(source.id)?.token) }
    func select(_ id: UUID) { selectedID = id; do { try local.save(id, key: "selected-source") } catch { self.error = error.localizedDescription } }
    func save(_ source: RemoteSource) throws {
        _ = try ServerEndpoint(source.address)
        if let index = sources.firstIndex(where: { $0.id == source.id }) {
            if sources[index].address != source.address { try CredentialVault.delete(source.id); authenticated.remove(source.id); local.clear(source: source.id); sourceStates[source.id] = "需要登录"; authRevision += 1 }
            sources[index] = source
        } else { sources.append(source); sourceStates[source.id] = "需要登录" }
        if selectedID == nil { selectedID = source.id }
        try local.save(sources, key: "sources")
    }
    func move(from: IndexSet, to: Int) { sources.move(fromOffsets: from, toOffset: to); do { try local.save(sources, key: "sources") } catch { self.error = error.localizedDescription } }
    func remove(_ source: RemoteSource) throws {
        try CredentialVault.delete(source.id); local.clear(source: source.id); authenticated.remove(source.id)
        sources.removeAll { $0.id == source.id }; sourceStates[source.id] = nil
        if selectedID == source.id { selectedID = sources.first?.id }
        try local.save(sources, key: "sources"); authRevision += 1
    }
    func logout(_ source: RemoteSource) async throws {
        if let cred = try CredentialVault.read(source.id), cred.kind == "app" { _ = try await client(source).call("mobile-tokens/self", method: "DELETE") }
        try CredentialVault.delete(source.id); authenticated.remove(source.id); sourceStates[source.id] = "需要登录"; authRevision += 1
    }
    func connect(_ source: RemoteSource, username: String, password: String, suppliedToken: String = "") async throws {
        let base = try APIClient(endpoint: ServerEndpoint(source.address))
        let health = try await base.health()
        var credential: Credential
        if !suppliedToken.isEmpty { credential = Credential(token: suppliedToken.trimmingCharacters(in: .whitespacesAndNewlines), kind: suppliedToken.hasPrefix("artex_app_") ? "app" : "jwt") }
        else {
            let response = try await base.call("auth/login", method: "POST", body: .object(["username": .string(username), "password": .string(password)]))
            let token = response["token"].string
            guard !token.isEmpty else { throw APIError.invalidResponse }
            credential = Credential(token: token, kind: "jwt")
            if health["capabilities"]["mobile_tokens_v1"].bool {
                let result = try await client(source, token: token).call("mobile-tokens", method: "POST", body: .object(["name": .string("ARTEX · iPhone 17"), "expires_in_days": .integer(90)]))
                guard !result["token"].string.isEmpty else { throw APIError.invalidResponse }
                credential = Credential(token: result["token"].string, kind: "app", tokenID: result["id"].string, expiresAt: result["expires_at"].string)
            }
        }
        _ = try await client(source, token: credential.token).tasks()
        try CredentialVault.save(credential, id: source.id)
        authenticated.insert(source.id); sourceStates[source.id] = "可连接"; authRevision += 1
        var verified = source; verified.version = health["version"].string; verified.verifiedAt = Date(); try save(verified)
    }
    func check(_ source: RemoteSource) async throws {
        if demoMode { sourceStates[source.id] = "演示数据"; return }
        let c = try client(source); let health = try await c.health(); _ = try await c.tasks()
        var verified = source; verified.version = health["version"].string; verified.verifiedAt = Date(); try save(verified); sourceStates[source.id] = "可连接"
    }
    func report(_ error: Error, source: RemoteSource) -> String {
        if error is CancellationError { return "" }
        if (error as? APIError) == .unauthenticated { authenticated.remove(source.id); sourceStates[source.id] = "需要登录"; authRevision += 1 }
        else { sourceStates[source.id] = "连接异常" }
        return error.localizedDescription
    }
    func beginWrite(source: RemoteSource, task: String) -> Bool { writes.insert("\(source.id):\(task)").inserted }
    func endWrite(source: RemoteSource, task: String) { writes.remove("\(source.id):\(task)") }
    func writeBusy(source: RemoteSource, task: String) -> Bool { writes.contains("\(source.id):\(task)") }
}
