// @vitest-environment jsdom
// A message refused because a dialog has the Agent's terminal (dialog_open): while the chat holds an open card of the
// Agent's, that dialog is most likely the card's own, so the failed bubble says to answer the card above rather than to
// go to the computer (#14549). Without a card it keeps the generic words.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { OutboxEntry } from "../../lib/outbox";
import { OutboxBubbles } from "./OutboxBubbles";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const refused = (code: string, error: string): OutboxEntry => ({ id: "m1", text: "数字人出镜讲解", images: [], at: 1, state: "failed", code, error });

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const reason = (entry: OutboxEntry, cardAbove?: boolean) => {
  act(() => root.render(<OutboxBubbles mid="box" sid="s1" entries={[entry]} cardAbove={cardAbove} />));
  return host.querySelector('[role="alert"] span')?.textContent;
};

describe("a dialog_open refusal", () => {
  it("points to the card above when the chat holds one", () => {
    expect(reason(refused("dialog_open", t("lib-outbox.dialog_open")), true))
      .toBe(t("chat-outboxbubbles.failed", { error: t("lib-outbox.dialog_open_card") }));
  });

  it("keeps the generic words without a card", () => {
    expect(reason(refused("dialog_open", t("lib-outbox.dialog_open")), false))
      .toBe(t("chat-outboxbubbles.failed", { error: t("lib-outbox.dialog_open") }));
  });

  it("leaves other refusals as they are, card or not", () => {
    expect(reason(refused("input_draft", t("lib-outbox.input_draft")), true))
      .toBe(t("chat-outboxbubbles.failed", { error: t("lib-outbox.input_draft") }));
  });
});
