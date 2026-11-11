import Foundation
import Security
import CryptoKit

struct Credential: Codable {
    var token: String
    var kind: String
    var tokenID: String?
    var expiresAt: String?
}

enum CredentialVault {
    static let service = "com.youxiongs.artex.mobile"
    static func read(_ id: UUID) throws -> Credential? {
        var q = query(id); q[kSecReturnData as String] = true; q[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw VaultError(status) }
        return try JSONDecoder().decode(Credential.self, from: data)
    }
    static func save(_ value: Credential, id: UUID) throws {
        let data = try JSONEncoder().encode(value)
        let attrs: [String: Any] = [kSecValueData as String: data, kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
        let status = SecItemUpdate(query(id) as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound {
            var q = query(id); attrs.forEach { q[$0.key] = $0.value }
            let added = SecItemAdd(q as CFDictionary, nil)
            guard added == errSecSuccess else { throw VaultError(added) }
        } else if status != errSecSuccess { throw VaultError(status) }
    }
    static func delete(_ id: UUID) throws {
        let status = SecItemDelete(query(id) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw VaultError(status) }
    }
    private static func query(_ id: UUID) -> [String: Any] { [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: id.uuidString, kSecAttrSynchronizable as String: false] }
    struct VaultError: LocalizedError { let status: OSStatus; init(_ status: OSStatus) { self.status = status }; var errorDescription: String? { "无法访问本机 Keychain（\(status)），请解锁手机后重试。" } }
}

struct Snapshot<T: Codable>: Codable { var value: T; var savedAt: Date }

@MainActor final class LocalStore {
    private let directory: URL
    init() {
        directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("ARTEX", isDirectory: true)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
        var url = directory; var values = URLResourceValues(); values.isExcludedFromBackup = true; try? url.setResourceValues(values)
    }
    private func url(_ key: String) -> URL {
        let prefix = String(key.split(separator: ":", maxSplits: 1).first ?? "")
        let parent = UUID(uuidString: prefix) == nil ? directory : directory.appendingPathComponent(prefix, isDirectory: true)
        try? FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true, attributes: [.protectionKey: FileProtectionType.complete])
        let digest = SHA256.hash(data: Data(key.utf8)).map { String(format: "%02x", $0) }.joined()
        return parent.appendingPathComponent(digest + ".json")
    }
    func load<T: Codable>(_ type: T.Type, key: String) -> T? { guard let data = try? Data(contentsOf: url(key)) else { return nil }; return try? JSONDecoder().decode(type, from: data) }
    func save<T: Codable>(_ value: T, key: String) throws { try JSONEncoder().encode(value).write(to: url(key), options: [.atomic, .completeFileProtection]) }
    func remove(_ key: String) { try? FileManager.default.removeItem(at: url(key)) }
    func clear(source: UUID) {
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(source.uuidString, isDirectory: true))
    }
}
