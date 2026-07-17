import { describe, it, expect } from "vitest";
import {
  buildScheduleArgs,
  missingTemplateVars,
  templatePlaceholders,
  templateVarsFor,
} from "./scheduleArgs.js";

describe("templatePlaceholders", () => {
  it("lists each placeholder once, in order", () => {
    expect(templatePlaceholders("say {{server_name}} in {{minutes}}m ({{minutes}} again)")).toEqual([
      "server_name",
      "minutes",
    ]);
  });

  it("returns nothing for a body with no placeholders", () => {
    expect(templatePlaceholders("say hello")).toEqual([]);
    expect(templatePlaceholders("")).toEqual([]);
  });

  it("does not ask for the placeholders the panel fills in itself", () => {
    expect(templateVarsFor("say Backing up {{server_name}}.")).toEqual([]);
    expect(templateVarsFor("say Restarting in {{seconds}} seconds!")).toEqual(["seconds"]);
  });
});

describe("missingTemplateVars", () => {
  it("names the vars with no value", () => {
    expect(missingTemplateVars({ seconds: "" }, ["seconds"])).toEqual(["seconds"]);
    expect(missingTemplateVars({ seconds: "30" }, ["seconds"])).toEqual([]);
    expect(missingTemplateVars({}, ["seconds"])).toEqual(["seconds"]);
  });
});

describe("buildScheduleArgs", () => {
  // The editor sent only `minutes`, so the seeded "Restart countdown" template
  // went out to players as "say Restarting in  seconds!" and logged ok.
  it("sends every placeholder the chosen template uses, not just minutes", () => {
    const args = buildScheduleArgs({
      action: "message",
      args: { template_id: "t1", seconds: "30" },
      templateVars: templateVarsFor("say Restarting in {{seconds}} seconds!"),
    });
    expect(args).toEqual({ template_id: "t1", seconds: "30" });
  });

  it("asks for nothing extra when the template only uses the server name", () => {
    const args = buildScheduleArgs({
      action: "message",
      args: { template_id: "t1" },
      templateVars: templateVarsFor("say Backing up {{server_name}}."),
    });
    expect(args).toEqual({ template_id: "t1" });
  });

  // runAction has always read args["warn"], but the editor never sent it, so
  // every restart scheduled here skipped the player countdown.
  it("sends warn for a restart", () => {
    const args = buildScheduleArgs({
      action: "restart",
      args: { skip_if_players: "true", warn: "true" },
    });
    expect(args).toEqual({ skip_if_players: "true", warn: "true" });
  });

  it("does not send warn for an update, which has no warned path", () => {
    const args = buildScheduleArgs({ action: "update", args: { skip_if_players: "false", warn: "true" } });
    expect(args).toEqual({ skip_if_players: "false" });
  });

  // Opening a schedule and pressing save used to wipe every arg the editor has
  // no field for.
  it("keeps args it does not manage", () => {
    const args = buildScheduleArgs({
      action: "restart",
      args: { skip_if_players: "true", warn: "true" },
      storedArgs: {
        skip_if_players: "true",
        warn: "true",
        backup_first: "true",
        target_id: "my-nas",
      },
    });
    expect(args.backup_first).toBe("true");
    expect(args.target_id).toBe("my-nas");
  });

  it("overwrites what it does manage", () => {
    const args = buildScheduleArgs({
      action: "restart",
      args: { skip_if_players: "false", warn: "false" },
      storedArgs: { skip_if_players: "true", warn: "true", backup_first: "true" },
    });
    expect(args).toEqual({ skip_if_players: "false", warn: "false", backup_first: "true" });
  });

  it("starts clean for a new schedule", () => {
    expect(buildScheduleArgs({ action: "backup", args: { target_id: "nas" } })).toEqual({
      target_id: "nas",
    });
  });
});
