package ingest

import "time"

// Observer receives sanitized aggregate outcomes only. Implementations must
// not receive request headers, tokens, source metadata, or event payloads.
type Observer interface {
	RecordIngestAccepted(events int, at time.Time)
	RecordIngestRejected(category ErrorCategory, databaseFailure bool, at time.Time)
}

func categoryForStatus(status int) ErrorCategory {
	switch status {
	case 401, 403:
		return CategoryForbidden
	case 409:
		return CategoryConflict
	case 413:
		return CategoryTooLarge
	case 429:
		return CategoryRateLimited
	case 503:
		return CategoryUnavailable
	case 507:
		return CategoryStorageFull
	default:
		return CategoryBadRequest
	}
}
