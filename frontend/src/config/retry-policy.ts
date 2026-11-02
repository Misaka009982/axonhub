/**
 * Frontend hint for the retry policy response timeouts, in seconds.
 *
 * The backend is the authority and no longer clamps these values: it accepts
 * any non-negative integer for both the global retry policy and the per-channel
 * override, leaving `server.llm_request_timeout` as the only effective ceiling.
 * A literal cannot be shared across Go and TypeScript, so this value is exported
 * for callers that still want a sanity ceiling on their numeric inputs.
 *
 * Keep the channel response schemas permissive about this upper bound. A schema
 * that rejected a value would fail to parse a channel the backend accepted,
 * which takes down the whole channel list instead of surfacing one bad field.
 */
export const RESPONSE_TIMEOUT_MAX_SECONDS = 86400;
