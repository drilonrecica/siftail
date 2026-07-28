"use strict";

(() => {
  const workspace = document.querySelector("[data-live-workspace]");
  if (!workspace || typeof window.EventSource !== "function") return;

  const renderedLimit = 1000;
  const pendingLimit = 2000;
  const scrollThreshold = 48;
  const form = workspace.querySelector("[data-live-filters]");
  const rows = workspace.querySelector("#live-rows");
  const scrollArea = workspace.querySelector("[data-live-scroll]");
  const status = workspace.querySelector("[data-live-status]");
  const notices = workspace.querySelector("[data-live-notices]");
  const announcement = workspace.querySelector("[data-live-announcement]");
  const pauseButton = workspace.querySelector("[data-live-pause]");
  const newestButton = workspace.querySelector("[data-live-newest]");
  const clearButton = workspace.querySelector("[data-live-clear]");
  const reconnectButton = workspace.querySelector("[data-live-reconnect]");
  const pendingButton = workspace.querySelector("[data-live-pending]");

  if (
    !(form instanceof HTMLFormElement) ||
    !rows ||
    !scrollArea ||
    !status ||
    !notices ||
    !announcement ||
    !pauseButton ||
    !newestButton ||
    !clearButton ||
    !reconnectButton ||
    !pendingButton
  ) {
    return;
  }

  let eventSource = null;
  let generation = 0;
  let paused = false;
  let trackingNewest = true;
  let pending = [];
  let textFilterTimer = 0;
  let reconnectTimer = 0;
  let renderedNoticeShown = false;
  let destroyed = false;

  const setStatus = (value) => {
    status.textContent = value;
    workspace.dataset.liveState = value.toLowerCase();
    announcement.textContent = value === "Paused"
      ? "Live view paused. Logs are still being stored."
      : `Live connection: ${value}.`;
  };

  const addNotice = (message, kind = "info") => {
    const existing = Array.from(notices.children).find(
      (node) => node.dataset.noticeKind === kind,
    );
    const notice = existing || document.createElement("p");
    notice.className = "live-notice";
    notice.dataset.noticeKind = kind;
    notice.textContent = message;
    if (!existing) notices.append(notice);
    announcement.textContent = message;
  };

  const clearNotice = (kind) => {
    notices.querySelector(`[data-notice-kind="${kind}"]`)?.remove();
  };

  const isNearNewest = () =>
    scrollArea.scrollHeight - scrollArea.scrollTop - scrollArea.clientHeight <= scrollThreshold;

  const updatePendingControl = () => {
    pendingButton.hidden = pending.length === 0;
    pendingButton.textContent = `↓ ${pending.length} new ${pending.length === 1 ? "event" : "events"}`;
  };

  const removeEmptyState = () => {
    rows.querySelector("[data-live-empty]")?.remove();
  };

  const ensureEmptyState = () => {
    if (rows.querySelector(".log-row") || rows.querySelector("[data-live-empty]")) return;
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.dataset.liveEmpty = "";
    const heading = document.createElement("h2");
    heading.textContent = "Waiting for new events from this source";
    const message = document.createElement("p");
    message.textContent = "Live events will appear here after they are committed.";
    empty.append(heading, message);
    rows.append(empty);
  };

  const sourceLabel = (event) => {
    const source = event.source || {};
    const application = source.application_label || source.application || "unknown";
    const service = source.service_label || source.service || "default";
    return `${application}/${service}`;
  };

  const safeLevel = (value) => {
    const allowed = new Set(["trace", "debug", "info", "warn", "error", "fatal", "unknown"]);
    return allowed.has(value) ? value : "unknown";
  };

  const makeLiveRow = (event) => {
    const id = Number(event.id);
    if (!Number.isSafeInteger(id) || id <= 0) return null;
    const level = safeLevel(event.level);
    const row = document.createElement("article");
    row.className = `log-row level-${level}`;
    row.dataset.eventId = String(id);
    row.tabIndex = -1;

    const toggle = document.createElement("button");
    toggle.className = "event-toggle";
    toggle.type = "button";
    toggle.setAttribute("aria-label", `Show details for event ${id}`);
    toggle.setAttribute("aria-expanded", "false");
    toggle.setAttribute("aria-controls", `event-detail-${id}`);
    toggle.setAttribute("hx-get", `/logs/events/${id}`);
    toggle.setAttribute("hx-target", `#event-detail-${id}`);
    toggle.setAttribute("hx-swap", "innerHTML");
    toggle.setAttribute("hx-disabled-elt", "this");
    toggle.textContent = "›";

    const timestamp = document.createElement("time");
    const date = new Date(Number(event.event_at_us) / 1000);
    if (!Number.isNaN(date.getTime())) {
      timestamp.dateTime = date.toISOString();
      timestamp.dataset.localTime = "";
      const options = {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        fractionalSecondDigits: 3,
      };
      timestamp.textContent = new Intl.DateTimeFormat([], options).format(date);
      timestamp.title = date.toLocaleString();
    } else {
      timestamp.textContent = "Unknown time";
    }

    const severity = document.createElement("span");
    severity.className = `severity severity-${level}`;
    severity.textContent = level;
    const source = document.createElement("span");
    source.className = "row-source";
    source.textContent = sourceLabel(event);
    const message = document.createElement("span");
    message.className = "row-message";
    message.textContent = typeof event.message === "string" ? event.message : "";
    if (event.message_truncated) message.title = "Preview truncated; open details for complete content.";
    const stream = document.createElement("span");
    stream.className = "row-stream";
    stream.textContent = typeof event.stream === "string" ? event.stream : "unknown";
    const detail = document.createElement("div");
    detail.id = `event-detail-${id}`;
    detail.className = "event-detail-slot";

    row.append(toggle, timestamp, severity, source, message, stream, detail);
    window.htmx?.process(row);
    return row;
  };

  const trimRendered = () => {
    const rendered = rows.querySelectorAll(".log-row");
    const excess = rendered.length - renderedLimit;
    if (excess <= 0) return;
    for (let index = 0; index < excess; index += 1) rendered[index].remove();
    if (!renderedNoticeShown) {
      renderedNoticeShown = true;
      addNotice(
        "Older rows were removed from this browser view. Persisted History is unchanged.",
        "rendered-limit",
      );
    }
  };

  const appendEvent = (event) => {
    const row = makeLiveRow(event);
    if (!row) return;
    removeEmptyState();
    rows.append(row);
    trimRendered();
  };

  const jumpToNewest = () => {
    if (!paused) {
      const queued = pending;
      pending = [];
      queued.forEach(appendEvent);
      updatePendingControl();
    }
    trackingNewest = true;
    scrollArea.scrollTo({
      top: scrollArea.scrollHeight,
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth",
    });
  };

  const closeSource = () => {
    generation += 1;
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
  };

  const browserTruncated = () => {
    pending = [];
    updatePendingControl();
    closeSource();
    setStatus("Truncated");
    addNotice(
      "Live view was truncated while you were away from the newest event. Use History to inspect the complete interval.",
      "truncated",
    );
    window.clearTimeout(reconnectTimer);
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = 0;
      if (!destroyed) connect();
    }, 0);
  };

  const queueEvent = (event) => {
    pending.push(event);
    if (pending.length > pendingLimit) {
      browserTruncated();
      return;
    }
    updatePendingControl();
  };

  const receiveLog = (event) => {
    let payload;
    try {
      payload = JSON.parse(event.data);
    } catch {
      addNotice("A malformed Live event was ignored.", "malformed");
      return;
    }
    if (paused || !trackingNewest) {
      queueEvent(payload);
      return;
    }
    appendEvent(payload);
    scrollArea.scrollTop = scrollArea.scrollHeight;
  };

  const receiveControl = (event) => {
    let control;
    try {
      control = JSON.parse(event.data);
    } catch {
      return;
    }
    switch (control.type) {
      case "heartbeat":
        break;
      case "possible_gap":
        addNotice(
          "Live reconnected without replay. Use History to inspect the possible gap.",
          "possible-gap",
        );
        break;
      case "truncated":
        closeSource();
        setStatus("Truncated");
        addNotice(
          "Live view was truncated. Use History to inspect the complete interval.",
          "truncated",
        );
        break;
      case "source_purged":
      case "source_removed":
        rows.replaceChildren();
        pending = [];
        updatePendingControl();
        ensureEmptyState();
        addNotice("This source changed and its previous rows are no longer retained.", "source-change");
        break;
      case "session_invalid":
        closeSource();
        window.location.assign(`/login?return=${encodeURIComponent(window.location.pathname + window.location.search)}&expired=1`);
        break;
      case "unavailable":
        closeSource();
        setStatus("Disconnected");
        addNotice("Live streaming is temporarily unavailable.", "unavailable");
        break;
      case "shutdown":
        closeSource();
        setStatus("Disconnected");
        addNotice("Siftail is shutting down. Reconnect after it restarts.", "shutdown");
        break;
    }
  };

  const connect = () => {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = 0;
    closeSource();
    if (destroyed) return;
    clearNotice("unavailable");
    const currentGeneration = generation;
    setStatus("Connecting");
    const source = new EventSource(workspace.dataset.streamUrl);
    eventSource = source;
    source.addEventListener("open", () => {
      if (destroyed || currentGeneration !== generation) return;
      setStatus(paused ? "Paused" : "Live");
    });
    source.addEventListener("log", (event) => {
      if (!destroyed && currentGeneration === generation) receiveLog(event);
    });
    source.addEventListener("control", (event) => {
      if (!destroyed && currentGeneration === generation) receiveControl(event);
    });
    source.addEventListener("error", () => {
      if (destroyed || currentGeneration !== generation) return;
      setStatus(source.readyState === EventSource.CLOSED ? "Disconnected" : "Reconnecting");
    });
  };

  const clearView = () => {
    rows.replaceChildren();
    pending = [];
    renderedNoticeShown = false;
    updatePendingControl();
    clearNotice("rendered-limit");
    ensureEmptyState();
    announcement.textContent = "Live browser view cleared. Persisted logs were not deleted.";
  };

  const togglePause = () => {
    paused = !paused;
    pauseButton.textContent = paused ? "Resume" : "Pause";
    if (paused) {
      setStatus("Paused");
      addNotice("Live view paused. Logs are still being stored.", "paused");
      return;
    }
    clearNotice("paused");
    setStatus(eventSource?.readyState === EventSource.OPEN ? "Live" : "Reconnecting");
    if (trackingNewest) jumpToNewest();
  };

  const streamURLFromForm = () => {
    const values = new URLSearchParams();
    const source = form.elements.namedItem("source");
    if (source instanceof HTMLSelectElement && source.value) values.set("source", source.value);
    const levels = form.elements.namedItem("levels");
    if (levels instanceof HTMLInputElement && levels.value) {
      levels.value.split(",").forEach((value) => values.append("level", value));
    }
    const streams = form.elements.namedItem("streams");
    if (streams instanceof HTMLInputElement && streams.value) {
      streams.value.split(",").forEach((value) => values.append("stream", value));
    }
    const contains = form.elements.namedItem("contains");
    if (contains instanceof HTMLInputElement && contains.value) values.set("contains", contains.value);
    const query = values.toString();
    return query ? `/logs/live/stream?${query}` : "/logs/live/stream";
  };

  const pageURLFromForm = () => {
    const values = new URLSearchParams(new FormData(form));
    for (const [key, value] of Array.from(values.entries())) {
      if (!value) values.delete(key);
    }
    return `/logs?${values.toString()}`;
  };

  const applyFilters = () => {
    workspace.dataset.streamUrl = streamURLFromForm();
    window.history.replaceState(null, "", pageURLFromForm());
    clearView();
    addNotice("Live filters changed. Showing newly committed matching events.", "filters");
    connect();
  };

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    window.clearTimeout(textFilterTimer);
    window.clearTimeout(reconnectTimer);
    applyFilters();
  });
  form.addEventListener("change", (event) => {
    if (event.target?.matches?.("[data-text-filter]")) return;
    applyFilters();
  });
  form.addEventListener("input", (event) => {
    if (!event.target?.matches?.("[data-text-filter]")) return;
    window.clearTimeout(textFilterTimer);
    textFilterTimer = window.setTimeout(applyFilters, 400);
  });
  scrollArea.addEventListener("scroll", () => {
    const atNewest = isNearNewest();
    if (atNewest && !trackingNewest && !paused) {
      trackingNewest = true;
      jumpToNewest();
    } else if (!atNewest) {
      trackingNewest = false;
    }
  }, { passive: true });
  pauseButton.addEventListener("click", togglePause);
  newestButton.addEventListener("click", jumpToNewest);
  pendingButton.addEventListener("click", jumpToNewest);
  clearButton.addEventListener("click", clearView);
  reconnectButton.addEventListener("click", () => {
    addNotice("Live was manually reconnected; a delivery gap may exist.", "possible-gap");
    connect();
  });

  const keyboard = (event) => {
    const target = event.target;
    if (
      target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement ||
      target?.isContentEditable
    ) return;
    const key = event.key.toLowerCase();
    if (key === "h") {
      event.preventDefault();
      window.location.assign("/logs");
    } else if (event.code === "Space") {
      event.preventDefault();
      togglePause();
    } else if (key === "g") {
      event.preventDefault();
      jumpToNewest();
    } else if (key === "j" || key === "k") {
      const available = Array.from(rows.querySelectorAll(".log-row"));
      if (available.length === 0) return;
      event.preventDefault();
      const current = available.indexOf(document.activeElement);
      const index = key === "j"
        ? Math.min(current + 1, available.length - 1)
        : Math.max(current < 0 ? available.length - 1 : current - 1, 0);
      available[index].focus({ preventScroll: true });
    } else if (event.key === "Enter" && document.activeElement?.matches?.(".log-row")) {
      document.activeElement.querySelector(".event-toggle")?.click();
    }
  };
  document.addEventListener("keydown", keyboard);
  window.addEventListener("pagehide", () => {
    destroyed = true;
    window.clearTimeout(textFilterTimer);
    closeSource();
    document.removeEventListener("keydown", keyboard);
  }, { once: true });

  connect();
})();
