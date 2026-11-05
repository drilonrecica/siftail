package auth

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

const eventDetailPreviewBytes = 16 << 10

func (b *Browser) eventDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		_ = b.ui.EventError(w, http.StatusNotFound, webui.EventErrorView{
			Message: "This event is no longer available.",
		})
		return
	}
	full, scope, valid := parseEventDetailQuery(r)
	if !valid {
		_ = b.ui.EventError(w, http.StatusBadRequest, webui.EventErrorView{
			Message: "The event detail request is invalid.",
		})
		return
	}
	if b.history == nil {
		b.renderEventReadError(w, r)
		return
	}
	event, err := b.history.Event(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		_ = b.ui.EventError(w, http.StatusNotFound, webui.EventErrorView{
			Message: "This event is no longer available.",
		})
		return
	}
	if err != nil {
		b.renderEventReadError(w, r)
		return
	}
	view, err := buildEventDetailView(event, full, scope, requestZone(r))
	if err != nil {
		b.renderEventReadError(w, r)
		return
	}
	if err := b.ui.EventDetail(w, http.StatusOK, view); err != nil {
		http.Error(w, "Event details are temporarily unavailable.", http.StatusInternalServerError)
	}
}

// parseEventDetailQuery reads ?full=1 and the current source scope, which
// the detail only uses to build its "Show events around this time" link.
func parseEventDetailQuery(r *http.Request) (bool, logs.SourceScope, bool) {
	values := r.URL.Query()
	for key, entries := range values {
		switch key {
		case "full", "server", "project", "environment", "application", "service":
		default:
			return false, logs.SourceScope{}, false
		}
		if len(entries) != 1 {
			return false, logs.SourceScope{}, false
		}
	}
	scope, err := logs.ParseSourceScope(values)
	if err != nil {
		return false, logs.SourceScope{}, false
	}
	fullValues, present := values["full"]
	if !present {
		return false, scope, true
	}
	if fullValues[0] != "1" {
		return false, logs.SourceScope{}, false
	}
	return true, scope, true
}

func (b *Browser) renderEventReadError(w http.ResponseWriter, r *http.Request) {
	_ = b.ui.EventError(w, http.StatusServiceUnavailable, webui.EventErrorView{
		Message:   "The database is temporarily unavailable.",
		RequestID: web.RequestIDFromContext(r.Context()),
	})
}

func buildEventDetailView(
	event logs.HistoryEvent,
	full bool,
	scope logs.SourceScope,
	zone displayZone,
) (webui.EventDetailView, error) {
	attributes, err := stableAttributes(event.AttributesJSON)
	if err != nil {
		return webui.EventDetailView{}, fmt.Errorf("format event attributes: %w", err)
	}
	message, messageTruncated := detailText([]byte(event.MessageText), full)
	raw, rawTruncated := detailText(event.MessageRaw, full)
	attributesText, attributesTruncated := detailText(attributes, full)
	detailID := fmt.Sprintf("event-detail-%d", event.ID)
	fullValues := url.Values{"full": {"1"}}
	scope.Set(fullValues)
	view := webui.EventDetailView{
		ID:                  event.ID,
		DetailID:            detailID,
		FullURL:             fmt.Sprintf("/logs/events/%d?", event.ID) + fullValues.Encode(),
		SurroundingURL:      surroundingURL(scope, time.UnixMicro(event.EventAtUS), event.ID),
		Full:                full,
		Message:             message,
		MessageBytes:        len([]byte(event.MessageText)),
		MessageTruncated:    messageTruncated,
		Attributes:          attributesText,
		AttributesBytes:     len(event.AttributesJSON),
		AttributesTruncated: attributesTruncated,
		Raw:                 raw,
		RawBytes:            len(event.MessageRaw),
		RawTruncated:        rawTruncated,
	}
	view.SourceFields = sourceDetailFields(event)
	view.TimingFields = timingDetailFields(event, zone)
	view.SeverityFields = []webui.DetailField{
		{Label: "Level", Value: string(event.Level)},
		{Label: "Stream", Value: string(event.Stream)},
	}
	if event.OriginalLevel != nil {
		view.SeverityFields = append(view.SeverityFields,
			webui.DetailField{Label: "Original level", Value: *event.OriginalLevel})
	}
	view.CommonFields = commonDetailFields(event)
	return view, nil
}

func stableAttributes(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("attributes contain trailing data")
		}
		return nil, err
	}
	output, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return output, nil
}

func detailText(input []byte, full bool) (string, bool) {
	truncated := !full && len(input) > eventDetailPreviewBytes
	if truncated {
		input = input[:eventDetailPreviewBytes]
		for len(input) > 0 && !utf8.Valid(input) {
			input = input[:len(input)-1]
		}
	}
	return strings.ToValidUTF8(string(input), "\uFFFD"), truncated
}

func sourceDetailFields(event logs.HistoryEvent) []webui.DetailField {
	fields := []webui.DetailField{
		{Label: "Server", Value: event.ServerName},
		{Label: "Project", Value: event.ProjectLabel},
		{Label: "Environment", Value: event.EnvironmentLabel},
		{Label: "Application", Value: event.ApplicationLabel},
		{Label: "Service", Value: event.ServiceLabel},
	}
	if event.SourceAlias != nil {
		fields = append(fields, webui.DetailField{Label: "Source alias", Value: *event.SourceAlias})
	}
	if event.ContainerID != nil {
		fields = append(fields, webui.DetailField{Label: "Container ID", Value: *event.ContainerID})
	}
	if event.ContainerName != nil {
		fields = append(fields, webui.DetailField{Label: "Container name", Value: *event.ContainerName})
	}
	return fields
}

// timingDetailFields shows the event time in the display zone and in UTC,
// the received time, and the delivery delay (DESIGN.md §12.2). UTC keeps the
// exact RFC3339 value an operator can paste into another tool; when the
// display zone is UTC the duplicate row is left out.
func timingDetailFields(event logs.HistoryEvent, zone displayZone) []webui.DetailField {
	eventAt := time.UnixMicro(event.EventAtUS).UTC()
	receivedAt := time.UnixMicro(event.ReceivedAtUS).UTC()
	fields := make([]webui.DetailField, 0, 4)
	if !zone.isUTC() {
		fields = append(fields, webui.DetailField{
			Label:    "Event time (" + zone.name + ")",
			Value:    eventAt.In(zone.loc).Format("02 Jan 2006 15:04:05.000000"),
			Datetime: datetime(eventAt),
		})
	}
	fields = append(fields,
		webui.DetailField{
			Label: "Event time (UTC)", Value: datetime(eventAt), Datetime: datetime(eventAt),
		},
		webui.DetailField{
			Label:    "Received time (" + zone.name + ")",
			Value:    receivedAt.In(zone.loc).Format("02 Jan 2006 15:04:05.000000"),
			Datetime: datetime(receivedAt),
		},
		webui.DetailField{Label: "Delivery delay", Value: receivedAt.Sub(eventAt).String()},
	)
	return fields
}

func commonDetailFields(event logs.HistoryEvent) []webui.DetailField {
	fields := make([]webui.DetailField, 0, 8)
	addString := func(label string, value *string) {
		if value != nil {
			fields = append(fields, webui.DetailField{Label: label, Value: *value})
		}
	}
	addString("Logger", event.Logger)
	addString("Request ID", event.RequestID)
	addString("Error type", event.ErrorType)
	addString("HTTP method", event.HTTPMethod)
	addString("HTTP path", event.HTTPPath)
	if event.HTTPStatus != nil {
		fields = append(fields, webui.DetailField{
			Label: "HTTP status", Value: strconv.FormatInt(*event.HTTPStatus, 10),
		})
	}
	if event.DurationMS != nil {
		fields = append(fields, webui.DetailField{
			Label: "Duration", Value: strconv.FormatFloat(*event.DurationMS, 'f', -1, 64) + " ms",
		})
	}
	addString("Source event ID", event.SourceEventID)
	return fields
}
