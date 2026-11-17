import XCTest

/// A page opened from inside another is a secondary page (#9971): it always has Back, Back returns to the page it came
/// from, and the sidebar's choice stays where it was. A page picked in the sidebar is a root (#8359, #8603; covered by
/// `SidebarRootsUITests`). Here, in the Sidebar layout:
/// - a task's card in the main conversation pushes its Agent's chat (it was made a root: no Back, the sidebar on the Agent);
/// - under iPhone Duo's side bars, Back stays where it is when the Agent comes up: a task still starting puts it at the top
///   of the side column too (`DetailHeader`), not at the top left;
/// - a rightward swipe from the middle of the page goes back (iOS 26's swipe back from anywhere), on the Agent's chat and
///   on the task's page that hides the system bar.
/// Against Fixtures/dispatch_hub.py (PERSONA_MOCK_HUB). `-sideBars YES` lays the pages out as under the Duo's side bars on
/// any simulator. PERSONA_SHOTS=<dir> keeps the screens (PERSONA_SHOTS_DEVICE names the device; PERSONA_SHOTS_EXTERNAL=1
/// asks the host for them, as the open Duo's own come out black).
final class SecondaryPageBackTests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private static let widgetAgent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    private struct Page: Decodable, CustomStringConvertible {
        var root: String
        var path: Int
        var description: String { "root \(root), path \(path)" }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs Fixtures/dispatch_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    // MARK: - Tests

    /// The task's card pushes the Agent's chat on the conversation: Back is there and leads back; the sidebar's root is the
    /// conversation all along.
    @MainActor func testATaskCardPushesItsAgentWithBack() throws {
        try cardPushes(sideBars: false)
    }

    /// The same under side bars (the system's bar holds Back, at the top of the side column on the Duo).
    @MainActor func testATaskCardPushesItsAgentWithBackUnderSideBars() throws {
        try cardPushes(sideBars: true)
    }

    /// Under side bars Back is where the Agent's chat has it before the Agent is up: the task's starting page has it in the
    /// system's bar, at the same place, and no ‹ of its own at the top left.
    @MainActor func testUnderSideBarsBackStaysPutWhileTheAgentStarts() throws {
        launch(sideBars: true)
        guard wide else { throw XCTSkip("pushed beside the sidebar in regular width only (iPad, iPhone Duo open)") }
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        let agentBack = oneBack("the Agent's chat")
        shot("agent-sidebars")
        agentBack.tap()
        assertConversation("Back from the Agent's chat")

        link("todo_dispatch_starting").tap()
        XCTAssertTrue(app.staticTexts["正在开 Agent"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        let startingBack = oneBack("the starting task's page")
        XCTAssertEqual(startingBack.frame.midX, agentBack.frame.midX, accuracy: 2,
                       "Back moved: \(startingBack.frame) on the starting page, \(agentBack.frame) on the Agent's chat")
        XCTAssertEqual(startingBack.frame.midY, agentBack.frame.midY, accuracy: 2,
                       "Back moved: \(startingBack.frame) on the starting page, \(agentBack.frame) on the Agent's chat")
        shot("starting-sidebars")
        startingBack.tap()
        assertConversation("Back from the starting task's page")
    }

    /// With the page's own header (bars across the top): the starting page's ‹ is its header's, one, and leads back.
    @MainActor func testTheStartingPageHasOneBack() throws {
        launch(sideBars: false)
        guard wide else { throw XCTSkip("pushed beside the sidebar in regular width only; a phone's sheet: TaskPageBackTests") }
        link("todo_dispatch_starting").tap()
        XCTAssertTrue(app.staticTexts["正在开 Agent"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        let back = oneBack("the starting task's page")
        shot("starting-header")
        back.tap()
        assertConversation("Back from the starting task's page")
    }

    /// A rightward swipe from the middle of the page goes back: on the Agent's chat (its header hides the system bar), and
    /// in regular width on the starting task's page (its own header, the bar hidden too).
    @MainActor func testASwipeFromTheMiddleGoesBack() throws {
        launch(sideBars: false)
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        sleep(1)
        swipeRight(from: app.buttons.matching(identifier: "chat.title").firstMatch, below: 160)
        assertConversation("a swipe from the middle of the Agent's chat")

        guard wide else { return }
        link("todo_dispatch_starting").tap()
        let starting = app.staticTexts["正在开 Agent"].firstMatch
        XCTAssertTrue(starting.waitForExistence(timeout: 10), app.debugDescription)
        sleep(1)
        swipeRight(from: starting, below: 0)
        assertConversation("a swipe from the middle of the starting task's page")
    }

    // MARK: - Steps

    @MainActor private func cardPushes(sideBars: Bool) throws {
        launch(sideBars: sideBars)
        let before = page()
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        if let before {
            let pushed = settledPage { $0.path == before.path + 1 }
            XCTAssertEqual(pushed?.root, before.root, "the card changed the sidebar's root: \(before) → \(pushed.map(\.description) ?? "-")")
            XCTAssertEqual(pushed?.path, before.path + 1, "the card did not push the Agent's chat: \(before) → \(pushed.map(\.description) ?? "-")")
        }
        XCTAssertFalse(app.descendants(matching: .any)["chat-sidebar-button"].exists, "the sidebar's button on a pushed chat")
        let back = oneBack("the Agent's chat")
        shot(sideBars ? "card-pushed-sidebars" : "card-pushed")
        back.tap()
        assertConversation("Back from the Agent's chat")
        if let before {
            let popped = settledPage { $0.path == before.path }
            XCTAssertEqual(popped?.root, before.root, "Back left the sidebar on another root: \(popped.map(\.description) ?? "-")")
        }
        shot(sideBars ? "card-back-sidebars" : "card-back")
    }

    // MARK: - Helpers

    @MainActor private func launch(sideBars: Bool) {
        app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url, "-holdChromeOpen", "YES", "-ui.scheme", "system", "-tab", "assistant",
                               "-dev.openChat", "", "-openAssistant", Self.main, "-app.navigation", "sidebar", "-probes", "YES",
                               "-sideBars", sideBars ? "YES" : "NO"]
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1"
        app.launchPinned()
        XCTAssertTrue(conversation.waitForExistence(timeout: 20), app.debugDescription)
    }

    @MainActor private var wide: Bool { app.windows.firstMatch.frame.width > 700 }

    /// The main conversation's In Progress entry: the conversation is in front.
    @MainActor private var conversation: XCUIElement { app.buttons["action-bar.running"] }

    @MainActor private func page() -> Page? { app.probe("sidebar-page", as: Page.self) }

    @MainActor private func settledPage(_ done: (Page) -> Bool) -> Page? {
        let deadline = Date.now.addingTimeInterval(10)
        var last = page()
        while Date.now < deadline {
            if let p = last, done(p) { return p }
            Thread.sleep(forTimeInterval: 0.3)
            last = page()
        }
        return last
    }

    /// The task's card, scrolled clear of the top bar and the dock.
    @MainActor private func link(_ id: String) -> XCUIElement {
        let link = app.buttons["dispatch.task.\(id)"]
        let tabs = app.buttons["shell-assistant"]
        let dock = app.otherElements["action-bar"]
        let deadline = Date.now.addingTimeInterval(30)
        while Date.now < deadline {
            if link.exists {
                let frame = link.frame
                let top = tabs.exists ? tabs.frame.maxY : app.frame.minY
                let bottom = dock.exists ? dock.frame.minY : app.frame.maxY
                if link.isHittable, frame.minY >= top, frame.maxY <= bottom { return link }
                let at = link.exists ? link : app.windows.firstMatch
                if frame.minY < top { at.swipeDown(velocity: .slow) } else { at.swipeUp(velocity: .slow) }
            }
            usleep(400_000)
        }
        XCTFail("no card for \(id):\n\(app.debugDescription)")
        return link
    }

    @MainActor private func assertChat(_ session: String) {
        let header = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10), "the Agent's chat did not open:\n\(app.debugDescription)")
        let deadline = Date.now.addingTimeInterval(10)
        while (header.value as? String) != session && Date.now < deadline { usleep(200_000) }
        XCTAssertEqual(header.value as? String, session, "opened another Agent")
    }

    /// The page's one way back, hittable, read once the page has settled.
    @MainActor private func oneBack(_ step: String) -> XCUIElement {
        let backs = app.buttons.matching(NSPredicate(format: "label IN %@", ["返回", "Back"]))
        XCTAssertTrue(backs.firstMatch.waitForExistence(timeout: 10), "\(step): no Back:\n\(app.debugDescription)")
        sleep(1)
        let shown = backs.allElementsBoundByIndex.filter { $0.exists && $0.isHittable }
        XCTAssertEqual(shown.count, 1, "\(step): \(shown.count) Backs:\n\(app.debugDescription)")
        return shown.first ?? backs.firstMatch
    }

    @MainActor private func assertConversation(_ step: String) {
        let deadline = Date.now.addingTimeInterval(10)
        while !(conversation.exists && conversation.isHittable) && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(conversation.isHittable, "\(step) did not return to the main conversation:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons.matching(identifier: "chat.title").firstMatch.exists, "\(step): the Agent's chat is still up")
    }

    /// A slow rightward drag that starts in the middle of the page (`below` points under the element, at its middle
    /// horizontally), far from the leading edge, the way a thumb swipes back.
    @MainActor private func swipeRight(from element: XCUIElement, below: CGFloat) {
        XCTAssertTrue(element.waitForExistence(timeout: 10), app.debugDescription)
        let window = app.windows.firstMatch
        let frame = element.frame
        let start = CGVector(dx: frame.midX, dy: frame.midY + below)
        let origin = window.coordinate(withNormalizedOffset: .zero)
        origin.withOffset(start)
            .press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: min(window.frame.maxX - 8, start.dx + 320), dy: start.dy)),
                   withVelocity: .slow, thenHoldForDuration: 0)
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone")
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let file = "back-\(device)-\(appearance)-\(name).png"
        let folder = URL(fileURLWithPath: dir)
        sleep(1)
        guard env["PERSONA_SHOTS_EXTERNAL"] != nil else {
            try? app.screenshot().pngRepresentation.write(to: folder.appendingPathComponent(file))
            return
        }
        try? file.write(to: folder.appendingPathComponent("want"), atomically: true, encoding: .utf8)
        let target = folder.appendingPathComponent(file)
        let deadline = Date.now.addingTimeInterval(15)
        while Date.now < deadline, !FileManager.default.fileExists(atPath: target.path) { Thread.sleep(forTimeInterval: 0.2) }
    }
}
