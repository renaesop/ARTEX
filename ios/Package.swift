// swift-tools-version: 6.2
import PackageDescription
let package = Package(name: "ARTEXCore", platforms: [.macOS(.v14), .iOS(.v26)], products: [.library(name: "ARTEXCore", targets: ["ARTEXCore"])], targets: [.target(name: "ARTEXCore"), .testTarget(name: "ARTEXCoreTests", dependencies: ["ARTEXCore"])], swiftLanguageModes: [.v5])
