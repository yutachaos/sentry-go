package sentryotel

import (
	"context"
	"fmt"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/otel/interal/utils"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type sentryPropagator struct{}

// Inject set tracecontext from the Context into the carrier.
func (p sentryPropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	fmt.Printf("\n--- Propagator Inject\nContext: %#v\nCarrier: %#v\n", ctx, carrier)

	spanContext := trace.SpanContextFromContext(ctx)

	var sentrySpan *sentry.Span

	if spanContext.IsValid() {
		sentrySpan, _ = sentrySpanMap.Get(spanContext.SpanID())
	} else {
		sentrySpan = nil
	}

	// Propagate sentry-trace header
	if sentrySpan != nil {
		// Sentry span exists => generate "sentry-trace" from it
		carrier.Set(sentry.SentryTraceHeader, sentrySpan.ToSentryTrace())
	} else {
		// No span => propagate the incoming sentry-trace header, if exists
		sentryTraceHeader, _ := ctx.Value(utils.SentryTraceHeaderKey()).(string)
		if sentryTraceHeader != "" {
			carrier.Set(sentry.SentryTraceHeader, sentryTraceHeader)
		}
	}

	// Propagate baggage header
	sentryBaggageStr := ""
	// TODO(anton): this is basically the isTransaction check. Do we actually need it?
	if sentrySpan != nil && len(sentrySpan.TraceID) > 0 {
		sentryBaggageStr = sentrySpan.ToBaggage().String()
	}
	sentryBaggage, _ := baggage.Parse(sentryBaggageStr)

	// Merge the baggage values
	finalBaggage := baggage.FromContext(ctx)
	for _, member := range sentryBaggage.Members() {
		var err error
		finalBaggage, err = finalBaggage.SetMember(member)
		if err != nil {
			continue
		}
	}

	if finalBaggage.Len() > 0 {
		carrier.Set(sentry.SentryBaggageHeader, finalBaggage.String())
	}
}

// Extract reads cross-cutting concerns from the carrier into a Context.
func (p sentryPropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	fmt.Printf("\n--- Propagator Extract\nContext: %#v\nCarrier: %#v\n", ctx, carrier)

	sentryTraceHeader := carrier.Get(sentry.SentryTraceHeader)

	if sentryTraceHeader != "" {
		ctx = context.WithValue(ctx, utils.SentryTraceHeaderKey(), sentryTraceHeader)
		if traceparentData, valid := sentry.ExtractSentryTrace([]byte(sentryTraceHeader)); valid {

			// TODO(anton): Do we need to set trace parent context somewhere here?
			// Like SENTRY_TRACE_PARENT_CONTEXT_KEY in JS

			spanContextConfig := trace.SpanContextConfig{
				TraceID:    trace.TraceID(traceparentData.TraceID),
				SpanID:     trace.SpanID(traceparentData.ParentSpanID),
				TraceFlags: trace.FlagsSampled,
				// TODO(anton): wtf is this
				Remote: true,
			}
			ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(spanContextConfig))
		}
	}

	baggageHeader := carrier.Get(sentry.SentryBaggageHeader)
	if baggageHeader != "" {
		// Preserve the original baggage
		parsedBaggage, err := baggage.Parse(baggageHeader)
		if err == nil {
			ctx = baggage.ContextWithBaggage(ctx, parsedBaggage)
		}
	}

	// The following cases should be already covered below:
	// * We can extract a valid dynamic sampling context (DSC) from the baggage
	// * No baggage header is present
	// * No Sentry-related values are present
	// * We cannot parse the baggage header for whatever reason
	dynamicSamplingContext, err := sentry.DynamicSamplingContextFromHeader([]byte(baggageHeader))
	if err != nil {
		// If there are any errors, create a new non-frozen one.
		dynamicSamplingContext = sentry.DynamicSamplingContext{Frozen: false}
	}
	ctx = context.WithValue(ctx, utils.DynamicSamplingContextKey(), dynamicSamplingContext)

	return ctx
}

func (p sentryPropagator) Fields() []string {
	return []string{sentry.SentryTraceHeader, sentry.SentryBaggageHeader}
}

func NewSentryPropagator() propagation.TextMapPropagator {
	return sentryPropagator{}
}