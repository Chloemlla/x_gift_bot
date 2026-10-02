package checkout

import "context"

type progressKey struct{}

// WithProgress reports completed workflow milestones, not elapsed-time estimates.
func WithProgress(ctx context.Context, report func(int, string)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}
func progress(ctx context.Context, percent int, message string) {
	if report, ok := ctx.Value(progressKey{}).(func(int, string)); ok {
		report(percent, message)
	}
}
