import Foundation
import Security

/// Secrets in the app's Keychain, on this iPhone only and readable once it
/// has been unlocked after a restart: the model's API keys and the pairing.
enum Keychain {
    static func get(_ account: String) -> String? {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Pinky.app,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
        ]
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    /// Saves value, or deletes the item when value is empty.
    @discardableResult
    static func set(_ account: String, _ value: String) -> Bool {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Pinky.app,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(q as CFDictionary)
        if value.isEmpty { return true }
        var add = q
        add[kSecValueData as String] = Data(value.utf8)
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        return SecItemAdd(add as CFDictionary, nil) == errSecSuccess
    }

    /// The account of a model service's API key, by the name it is saved
    /// under (ModelService.keyName, pilot.KeyName): the service's id, or
    /// the API's name for the custom entries, as keys were named before
    /// there were services.
    static func keyAccount(_ keyName: String) -> String { "key." + keyName }
}

/// The pairing with this iPhone's remotepairingd: {"udid", "pairing"
/// (base64)}. A developer build may carry one (build/Pairing.json), which
/// moves into the Keychain on the first start.
struct Pairing: Codable {
    var udid: String
    var pairing: String

    static func load() -> Pairing? {
        #if DEBUG
        forgetOnce()
        #endif
        if let s = Keychain.get("pairing"), let p = try? JSONDecoder().decode(Pairing.self, from: Data(s.utf8)) {
            return p
        }
        guard let url = Bundle.main.url(forResource: "Pairing", withExtension: "json"),
              let data = try? Data(contentsOf: url),
              let p = try? JSONDecoder().decode(Pairing.self, from: data), !p.pairing.isEmpty
        else { return nil }
        p.save()
        return p
    }

    #if DEBUG
    nonisolated(unsafe) private static var forgot = false

    /// A debug build launched with -pinkyForgetPairing YES drops the pairing
    /// the Keychain keeps (it outlives deleting the app), once per launch, so
    /// a test reaches the unpaired first screen.
    private static func forgetOnce() {
        guard !forgot else { return }
        forgot = true
        if UserDefaults.standard.bool(forKey: "pinkyForgetPairing") { Keychain.set("pairing", "") }
    }
    #endif

    func save() {
        if let d = try? JSONEncoder().encode(self) { Keychain.set("pairing", String(data: d, encoding: .utf8)!) }
    }
}
