/**
 * Lightweight debug logger.
 * Enabled by setting the DEBUG environment variable to any non-empty value.
 *
 * Usage:
 *   import { debug } from "../middleware/debug.ts";
 *   debug("[upload:123]", "rejected: file too large");
 */

const enabled = Boolean(Deno.env.get("DEBUG"));
const encoder = new TextEncoder();

export function debug(...args: unknown[]): void {
  if (!enabled) return;
  Deno.stderr.writeSync(encoder.encode(`${args.map(String).join(" ")}\n`));
}
