// Building a schedule's args map is where three separate bugs lived: a
// placeholder the editor never sent ({{seconds}}, broadcast to players as a
// gap), a warn flag it couldn't set (every scheduled restart skipped the player
// countdown), and args it destroyed on save (backup_first and target_id, wiped
// by opening a schedule and pressing save).
//
// All three were pure data-in, data-out mistakes that no Go test could see, so
// the logic lives here rather than inline in Schedules.svelte — here a test can
// reach it.
//
// The shape is dictated by runAction in internal/api/scheduler.go: args is an
// open map[string]string, and each action reads the keys it cares about.

// Placeholders the panel fills in itself, so the form never asks for them.
export const COMPUTED_VARS = ["server_name"];

// The {{key}} names a template body uses, in order, without duplicates.
export function templatePlaceholders(body) {
  return [...new Set([...(body || "").matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]))];
}

// The placeholders an admin has to supply a value for: what the body uses, minus
// the ones the panel fills in.
export function templateVarsFor(body) {
  return templatePlaceholders(body).filter((v) => !COMPUTED_VARS.includes(v));
}

// Template vars with no value yet. A message that still holds a placeholder at
// send time is refused by the runner, so catch it while someone can fix it.
export function missingTemplateVars(args, templateVars) {
  return templateVars.filter((v) => !args[v]);
}

// The args to send for a schedule.
//
// storedArgs is what the schedule already carries. Everything starts from it:
// the editor overwrites what it manages and leaves the rest alone, because
// runAction reads keys this form has no field for and an API client can set
// anything. Rebuilding from scratch is what threw those away.
export function buildScheduleArgs({ action, args = {}, storedArgs = {}, templateVars = [] }) {
  const out = { ...storedArgs };
  if (action === "backup") out.target_id = args.target_id;
  if (action === "command") out.command = args.command;
  if (action === "message") {
    out.template_id = args.template_id;
    for (const v of templateVars) out[v] = args[v];
  }
  if (action === "restart" || action === "update") out.skip_if_players = args.skip_if_players;
  if (action === "restart") out.warn = args.warn;
  return out;
}
