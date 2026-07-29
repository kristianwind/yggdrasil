import { describe, it, expect } from "vitest";
import { seedVarDefaults } from "./varDefaults.js";

describe("seedVarDefaults", () => {
  // The regression this exists for: a secret variable with no value in the
  // server's env reached the first render as undefined, and binding undefined
  // into PasswordField (whose prop declared a fallback) threw
  // props_invalid_value — blanking the whole settings form, not just that field.
  it("seeds a variable that is missing from the env entirely", () => {
    const variables = [
      { key: "SMTP_PASSWORD", type: "string", default: "" },
      { key: "SMTP_PORT", type: "string", default: "587" },
    ];
    const values = {}; // server predates these rune variables
    seedVarDefaults(variables, values);
    expect(values.SMTP_PASSWORD).toBe("");
    expect(values.SMTP_PORT).toBe("587");
    expect(values.SMTP_PASSWORD).not.toBeUndefined();
  });

  it("leaves an existing value alone, including an empty string", () => {
    const values = { A: "set", B: "" };
    seedVarDefaults([{ key: "A", default: "x" }, { key: "B", default: "y" }], values);
    expect(values.A).toBe("set");
    expect(values.B).toBe(""); // "" is a real value, not "unset"
  });

  it("does not invent a value when the variable has no default", () => {
    const values = {};
    seedVarDefaults([{ key: "NO_DEFAULT", type: "string" }], values);
    expect("NO_DEFAULT" in values).toBe(false);
  });

  it("coerces string bools coming back from env_json", () => {
    const values = { T: "true", F: "false" };
    seedVarDefaults([{ key: "T", type: "bool" }, { key: "F", type: "bool" }], values);
    expect(values.T).toBe(true);
    expect(values.F).toBe(false); // the bug this guards: "false" is truthy as a string
  });

  it("survives junk input rather than throwing", () => {
    expect(() => seedVarDefaults(null, {})).not.toThrow();
    expect(() => seedVarDefaults([null, { no: "key" }], {})).not.toThrow();
    expect(seedVarDefaults([{ key: "A", default: 1 }], null)).toBe(null);
  });
});
