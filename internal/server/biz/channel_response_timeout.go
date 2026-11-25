package biz

import (
	"fmt"

	"github.com/looplj/axonhub/internal/objects"
)

// NormalizeResponseTimeout validates the per-channel response timeout overrides
// and drops them unless the channel actually overrides the global timeouts.
//
// Inherit mode ignores the override values, so keeping them would persist
// numbers that no reader expects: a stale negative value would be accepted
// here but rejected by every client that schema-checks a channel, which breaks
// the channel list. Clearing them makes "inherited" mean the same thing in
// storage as it does at request time.
func NormalizeResponseTimeout(settings *objects.ChannelSettings) error {
	if settings == nil {
		return nil
	}

	if !settings.ResponseTimeout.IsCustom() {
		settings.ResponseTimeout = nil

		return nil
	}

	return ValidateResponseTimeout(settings.ResponseTimeout)
}

// ValidateResponseTimeout checks the per-channel response timeout overrides.
// A nil settings object, or one whose mode is inherit, means the channel follows
// the global retry policy and is always valid; any override values present in
// that mode are ignored here because NormalizeResponseTimeout drops them before
// they are stored.
//
// 配置值必须非负，本层不设上限，与全局重试策略一致。
// 流式与非流式各自的总时限，以及已启用的上游流空闲时限仍独立生效。
func ValidateResponseTimeout(settings *objects.ChannelResponseTimeoutSettings) error {
	if !settings.IsCustom() {
		return nil
	}

	if err := validateResponseTimeoutSeconds("streamFirstEventTimeoutSeconds", settings.StreamFirstEventTimeoutSeconds); err != nil {
		return err
	}

	return validateResponseTimeoutSeconds("nonStreamResponseTimeoutSeconds", settings.NonStreamResponseTimeoutSeconds)
}

func validateResponseTimeoutSeconds(name string, seconds *int) error {
	if seconds == nil {
		return nil
	}

	if *seconds < 0 {
		return fmt.Errorf("%s must be >= 0", name)
	}

	return nil
}
