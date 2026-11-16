//go:build !linux

package agent

import "context"

func collectPlatformFacts(ctx context.Context) (platformFacts, error) {
	if err := ctx.Err(); err != nil {
		return platformFacts{}, err
	}
	// Portable baseline facts are still returned by CollectSystemFacts. Host-
	// specific metrics are omitted until a platform implementation is added.
	return platformFacts{}, nil
}
