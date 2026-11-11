import XCTest

final class ARTEXUITests: XCTestCase {
    @MainActor func testTaskSessionAndHintFlow() throws {
        let app = XCUIApplication(); app.launchArguments = ["--demo"]; app.launch()
        XCTAssertTrue(app.buttons["task-52"].waitForExistence(timeout: 15))
        capture(app, name: "Tasks compact")
        app.buttons["task-52"].tap()
        capture(app, name: "Task overview")
        app.segmentedControls.buttons["Sessions"].tap()
        XCTAssertTrue(app.buttons["main-chat"].waitForExistence(timeout: 10))
        capture(app, name: "Sessions compact")
        app.buttons["main-chat"].tap()
        let input = app.textViews["chat-input"].exists ? app.textViews["chat-input"] : app.textFields["chat-input"]
        XCTAssertTrue(input.waitForExistence(timeout: 10))
        capture(app, name: "Main conversation")
        input.tap(); input.typeText("Please summarize progress")
        capture(app, name: "Chat keyboard")
        app.buttons["send-hint"].tap()
        XCTAssertTrue(app.staticTexts["已收到服务器回复"].waitForExistence(timeout: 10))
        let attachment = XCTAttachment(screenshot: app.screenshot()); attachment.name = "Main chat on iPhone 17"; attachment.lifetime = .keepAlways; add(attachment)
    }
    @MainActor private func capture(_ app: XCUIApplication, name: String) { let a = XCTAttachment(screenshot: app.screenshot()); a.name = name; a.lifetime = .keepAlways; add(a) }
    @MainActor func testFindingsAndCreation() throws {
        let app = XCUIApplication(); app.launchArguments = ["--demo"]; app.launch()
        XCTAssertTrue(app.tabBars.buttons["Findings"].waitForExistence(timeout: 10)); app.tabBars.buttons["Findings"].tap()
        XCTAssertTrue(app.buttons["finding-108"].waitForExistence(timeout: 10)); capture(app, name: "Findings compact")
        app.buttons["finding-108"].tap(); XCTAssertTrue(app.staticTexts["报告"].waitForExistence(timeout: 10)); capture(app, name: "Finding report")
        app.terminate(); app.launch()
        app.buttons["create-task"].tap()
        let name = app.textFields["task-name"]; XCTAssertTrue(name.waitForExistence(timeout: 10)); name.tap(); name.typeText("Demo mobile task")
        let goal = app.textViews["task-goal"].exists ? app.textViews["task-goal"] : app.textFields["task-goal"]; goal.tap(); goal.typeText("Review the demo fixture")
        app.buttons["submit-task"].tap()
        XCTAssertTrue(app.staticTexts["Demo mobile task"].waitForExistence(timeout: 10))
    }
}
