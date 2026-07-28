package audit

import "context"

type attributionContextKey struct{}

type Attribution struct {
	ActorType       ActorType
	AdministratorID *int64
	RequestID       string
}

func ContextWithAttribution(ctx context.Context, attribution Attribution) context.Context {
	return context.WithValue(ctx, attributionContextKey{}, attribution)
}

func AttributionFromContext(ctx context.Context) Attribution {
	attribution, ok := ctx.Value(attributionContextKey{}).(Attribution)
	if !ok {
		return Attribution{ActorType: ActorSystem}
	}
	return attribution
}

func InputFromContext(
	ctx context.Context,
	category Category,
	action string,
	outcome Outcome,
	metadata Metadata,
) Input {
	attribution := AttributionFromContext(ctx)
	return Input{
		Category: category, Action: action, Outcome: outcome,
		ActorType:       attribution.ActorType,
		AdministratorID: attribution.AdministratorID,
		Metadata:        metadata, RequestID: attribution.RequestID,
	}
}
