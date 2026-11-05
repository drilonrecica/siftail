package ingest

import "time"

// Observer receives sanitized aggregate outcomes only. Implementations must
// not receive request headers, tokens, source metadata, or event payloads.
type Observer interface {
	RecordIngestAccepted(events int, at time.Time)
	RecordIngestRejected(category ErrorCategory, databaseFailure bool, at time.Time)
	// RecordIngestConflicts reports records of one committed batch from the
	// trusted Server that were dropped because they reused a stable source
	// event ID for different content (ADR 0007). Only the count is passed.
	RecordIngestConflicts(serverID int64, dropped int, at time.Time)
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
	case 500:
		return CategoryInternal
	case 503:
		return CategoryUnavailable
	case 507:
		return CategoryStorageFull
	default:
		return CategoryBadRequest
	}
}
