import { describe, expect, it } from "vitest";
import { Emit } from "@guardian/chunkies";
import { bringTheDogIn } from "@guardian/chunkies-testkit";
import { ActionKind, actionName } from "../src/actions.ts";
import { wumRig } from "./rig.ts";

describe("the action vocabulary", () => {
  it("names every action kind this client can send", async () => {
    // The action verbs are the client's whole write surface, and the kind
    // rides every action span as a bare number. A verb whose kind has no
    // row in ActionKind reaches dashboards as "kind N" — this is the only
    // thing that notices the table going stale.
    const r = await wumRig({ role: "player", myDog: 0x9803n });
    await r.establish();
    await bringTheDogIn(r, 0x9803n);
    r.game.checkIn();
    r.moveTo(1);
    r.game.setBoost(true);
    await r.run(300);

    const sent = r.harness.emitted
      .filter((e) => e.code === Emit.intentSent)
      .map((e) => Number(e.a));
    expect(new Set(sent).size, "distinct kinds exercised").toBeGreaterThanOrEqual(4);
    const unnamed = [...new Set(sent)].filter((kind) => actionName(kind) === null);
    expect(unnamed, "action kinds without a name").toEqual([]);
  });

  it("spells the dashboard names", () => {
    expect(ActionKind).toEqual({ 1: "join", 3: "check_in", 4: "move_to", 8: "boost" });
    expect(actionName(4)).toBe("move_to");
    expect(actionName(99)).toBeNull();
  });
});
