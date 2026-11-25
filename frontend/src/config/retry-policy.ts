/**
 * Frontend hint for the retry policy response timeouts, in seconds.
 *
 * 后端接受全局及渠道覆盖的非负整数，不再强行钳制；
 * 所选请求类型的总时限及显式启用的上游流空闲时限仍独立生效。
 * A literal cannot be shared across Go and TypeScript, so this value is exported
 * for callers that still want a sanity ceiling on their numeric inputs.
 *
 * Keep the channel response schemas permissive about this upper bound. A schema
 * that rejected a value would fail to parse a channel the backend accepted,
 * which takes down the whole channel list instead of surfacing one bad field.
 */
export const RESPONSE_TIMEOUT_MAX_SECONDS = 86400;
