// Seeding for rune-variable forms (VarForm).
//
// Lives outside the component so it can run — and be tested — before the first
// render. That timing is the whole point: a variable with no value in the
// server's env (typically one added to the rune after the server was created)
// would otherwise reach the first render as `undefined`, and binding `undefined`
// into a child prop that declares a fallback throws Svelte's
// `props_invalid_value`, which takes down the entire form rather than one field.

/**
 * Fill in defaults and normalize types for a rune's variables, in place.
 *
 * @param {Array<{key:string,type?:string,default?:any}>} variables rune variables
 * @param {Record<string, any>} values the env object bound to the form
 * @returns {Record<string, any>} the same `values`, mutated
 */
export function seedVarDefaults(variables, values) {
  if (!Array.isArray(variables) || !values || typeof values !== "object") return values;
  for (const v of variables) {
    if (!v || !v.key) continue;
    if (values[v.key] === undefined && v.default !== undefined && v.default !== null) {
      values[v.key] = v.default;
    }
    // Bools round-trip through env_json as the *string* "true"/"false", and a
    // non-empty string like "false" is truthy — which would render the checkbox
    // as checked even when the stored value is false (so saving silently writes
    // "false" back). Coerce to a real boolean.
    if (v.type === "bool" && typeof values[v.key] === "string") {
      values[v.key] = values[v.key] === "true";
    }
  }
  return values;
}
