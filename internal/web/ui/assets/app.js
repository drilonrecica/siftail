"use strict";

(() => {
  // Every server-rendered time is already in the display zone (DESIGN.md
  // §12.2, ADR 0011 D7). The browser formats only Live rows and the
  // custom-range picker, both in the zone named by data-timezone on <main>,
  // with fixed 24-hour formats assembled from parts so a 12-hour or other
  // locale can never change the text or the column width.
  window.siftailTime = (() => {
    const requested =
      document.querySelector("main[data-timezone]")?.dataset.timezone || "UTC";
    const options = {
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      fractionalSecondDigits: 3,
    };
    let zone = requested;
    let format;
    try {
      format = new Intl.DateTimeFormat("en-US", { ...options, timeZone: zone });
    } catch {
      // The server validated the zone, but this browser's zone data may lack
      // it; UTC is the documented fallback.
      zone = "UTC";
      format = new Intl.DateTimeFormat("en-US", { ...options, timeZone: zone });
    }
    const parts = (date) => {
      const result = {};
      for (const part of format.formatToParts(date)) result[part.type] = part.value;
      return result;
    };
    // The zone's wall clock for an instant, read back as if it were UTC.
    const wallMs = (ms) => {
      const p = parts(new Date(ms));
      return Date.UTC(
        Number(p.year), Number(p.month) - 1, Number(p.day),
        Number(p.hour), Number(p.minute), Number(p.second),
        Number(p.fractionalSecond),
      );
    };
    return {
      zone,
      // 15:04:05.000, the row format.
      formatRowTime(date) {
        const p = parts(date);
        return `${p.hour}:${p.minute}:${p.second}.${p.fractionalSecond}`;
      },
      // 2006-01-02T15:04:05 in the zone, the datetime-local value.
      toWall(date) {
        const p = parts(date);
        return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}`;
      },
      // The instant a datetime-local value names in the zone, or null. The
      // second pass corrects the offset when the first guess crossed a
      // daylight-saving change.
      fromWall(value) {
        const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?/.exec(value);
        if (!match) return null;
        const guess = Date.UTC(
          Number(match[1]), Number(match[2]) - 1, Number(match[3]),
          Number(match[4]), Number(match[5]), Number(match[6] || 0),
        );
        const first = guess - (wallMs(guess) - guess);
        const result = guess - (wallMs(first) - first);
        return Number.isNaN(result) ? null : new Date(result);
      },
    };
  })();

  // Theme and density are server-rendered on <html> from the preference
  // cookie. This enhancement applies changes instantly and persists them in
  // the background; without it the form's noscript submit does the same.
  const prefsForm = document.querySelector("[data-prefs-form]");
  const applyTheme = (theme) => {
    if (theme === "dark" || theme === "light") {
      document.documentElement.dataset.theme = theme;
    } else {
      delete document.documentElement.dataset.theme;
    }
  };
  const applyDensity = (density) => {
    document.documentElement.dataset.density =
      density === "comfortable" ? "comfortable" : "compact";
  };
  // The keyboard module reads data-shortcuts on every key press, so turning
  // shortcuts off takes effect immediately (DESIGN.md §32.6).
  const applyShortcuts = (shortcuts) => {
    if (shortcuts === "off") {
      document.documentElement.dataset.shortcuts = "off";
    } else {
      delete document.documentElement.dataset.shortcuts;
    }
  };
  // The time zone control offers the browser's own zone beside UTC
  // (DESIGN.md §19.5). The server renders an already-chosen zone as an
  // option; when it is this browser's zone it is relabelled, not repeated.
  const timezoneSelect = document.querySelector("[data-timezone-select]");
  let browserZone = "";
  try {
    browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    browserZone = "";
  }
  if (timezoneSelect instanceof HTMLSelectElement &&
    /^[A-Za-z0-9_+/-]{1,64}$/.test(browserZone) &&
    browserZone !== "UTC" && browserZone !== "Etc/UTC") {
    const label = `Browser time zone (${browserZone})`;
    const existing = Array.from(timezoneSelect.options)
      .find((option) => option.value === browserZone);
    if (existing) {
      existing.textContent = label;
    } else {
      timezoneSelect.append(new Option(label, browserZone));
    }
  }
  const initialZone = timezoneSelect instanceof HTMLSelectElement
    ? timezoneSelect.value
    : "";

  if (prefsForm) {
    prefsForm.addEventListener("change", () => {
      const data = new FormData(prefsForm);
      applyTheme(String(data.get("theme")));
      applyDensity(String(data.get("density")));
      applyShortcuts(String(data.get("shortcuts")));
      // Times are rendered by the server, so a new zone reloads the page
      // once the cookie is saved (ADR 0011 D7).
      const zoneChanged = timezoneSelect instanceof HTMLSelectElement &&
        String(data.get("timezone")) !== initialZone;
      void fetch(prefsForm.action, {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
        },
        body: new URLSearchParams(data).toString(),
        credentials: "same-origin",
        redirect: "manual",
      }).then(() => {
        if (zoneChanged) window.location.reload();
      }).catch(() => {
        // The applied preference still styles this page; persistence retries
        // on the next change or full form submission.
      });
    });
  }

  // Success flashes auto-dismiss (DESIGN.md §26); warnings and errors stay
  // until the operator moves on.
  document.querySelectorAll("[data-flash-dismiss]").forEach((flash) => {
    window.setTimeout(() => {
      flash.closest(".flash-region")?.remove();
    }, 6000);
  });

  // The System menu is a native <details>; close it when focus or pointer
  // interaction leaves it so it behaves like a menu without ARIA takeover.
  const systemMenu = document.querySelector("[data-system-menu]");
  if (systemMenu) {
    document.addEventListener("click", (event) => {
      if (systemMenu.open && event.target instanceof Node &&
        !systemMenu.contains(event.target)) {
        systemMenu.open = false;
      }
    });
    // Escape reaches the menu through the keyboard module, the only
    // key-event listener (DESIGN.md §39).
    window.siftailKeyboard?.register((event) => {
      if (event.key !== "Escape" || !systemMenu.open ||
        !(event.target instanceof Node) || !systemMenu.contains(event.target)) {
        return false;
      }
      systemMenu.open = false;
      systemMenu.querySelector("summary")?.focus();
      return true;
    });
  }

  // The keyboard help's "shortcuts are off" note points at the preference:
  // open the System menu and focus the control.
  document.addEventListener("click", (event) => {
    if (!(event.target instanceof Element) ||
      !event.target.closest("[data-show-shortcuts-pref]")) {
      return;
    }
    event.target.closest("dialog")?.close();
    const menu = document.querySelector("[data-system-menu]");
    if (menu instanceof HTMLDetailsElement) menu.open = true;
    document.querySelector("[data-shortcuts-select]")?.focus();
  });

  if (window.htmx) {
    window.htmx.config.historyCacheSize = 0;
  }

  const updateListValue = (checkbox) => {
    const form = checkbox.closest("form");
    const name = checkbox.dataset.listFilter;
    if (!form || !name) return;
    const hidden = form.querySelector(`[data-list-value="${name}"]`);
    if (!hidden) return;
    const selected = Array.from(
      form.querySelectorAll(`[data-list-filter="${name}"]:checked`),
      (input) => input.value,
    );
    hidden.value = selected.join(",");
  };

  document.addEventListener(
    "change",
    (event) => {
      const target = event.target;
      if (!(target instanceof HTMLInputElement || target instanceof HTMLSelectElement)) {
        return;
      }
      if (target.matches("[data-list-filter]")) {
        updateListValue(target);
      }
      if (target.matches("[data-source-level]")) {
        const level = Number(target.dataset.sourceLevel);
        const form = target.closest("form");
        if (!form) return;
        form.querySelectorAll("[data-source-level]").forEach((select) => {
          if (Number(select.dataset.sourceLevel) > level) {
            select.value = "";
          }
        });
        const container = form.querySelector('select[name="container"]');
        if (container) container.value = "";
      }
    },
    { capture: true },
  );

  // Collapses a row's details. Focus stays on the row when the row itself
  // collapsed them (Enter on the selected row); otherwise it returns to the
  // row's toggle (DESIGN.md §13.6).
  const collapseDetails = (toggle) => {
    const slot = document.getElementById(toggle.getAttribute("aria-controls") || "");
    slot?.replaceChildren();
    toggle.setAttribute("aria-expanded", "false");
    toggle.textContent = "›";
    const row = toggle.closest(".log-row");
    if (document.activeElement !== row) toggle.focus({ preventScroll: true });
  };

  // A click on an expanded toggle (the chevron, or Enter on the row, which
  // clicks it) collapses. This runs in the capture phase so the toggle's own
  // htmx listener never sees the click: otherwise htmx would fetch the detail
  // again and re-expand the row after it collapsed (audit UX-03).
  document.addEventListener("click", (event) => {
    const toggle = event.target instanceof Element
      ? event.target.closest(".event-toggle[aria-expanded='true']")
      : null;
    if (!toggle) return;
    event.preventDefault();
    event.stopPropagation();
    collapseDetails(toggle);
  }, { capture: true });

  document.addEventListener("click", (event) => {
    const openExport = event.target.closest("[data-open-export]");
    if (openExport) {
      const dialog = document.querySelector("[data-export-dialog]");
      if (dialog instanceof HTMLDialogElement) {
        dialog.showModal();
        dialog.querySelector("select")?.focus();
      }
      return;
    }
    const closeExport = event.target.closest("[data-close-export]");
    if (closeExport) {
      const dialog = closeExport.closest("dialog");
      if (dialog instanceof HTMLDialogElement) {
        dialog.close();
        document.querySelector("[data-open-export]")?.focus();
      }
      return;
    }
    const collapse = event.target.closest("[data-collapse-detail]");
    if (collapse) {
      const toggle = collapse.closest(".log-row")?.querySelector(".event-toggle");
      if (toggle) {
        collapseDetails(toggle);
      } else {
        collapse.closest(".event-detail-slot")?.replaceChildren();
      }
      return;
    }
    const copy = event.target.closest("[data-copy-target]");
    if (copy) {
      const source = document.getElementById(copy.dataset.copyTarget);
      if (!source) return;
      const original = copy.textContent;
      if (!navigator.clipboard) {
        copy.textContent = "Copy unavailable";
        window.setTimeout(() => {
          copy.textContent = original;
        }, 1500);
        return;
      }
      navigator.clipboard.writeText(source.textContent).then(() => {
        copy.textContent = "Copied";
        window.setTimeout(() => {
          copy.textContent = original;
        }, 1500);
      }).catch(() => {
        copy.textContent = "Copy failed";
      });
      return;
    }
    const rangeButton = event.target.closest("[data-focus-range]");
    if (rangeButton) {
      rangeButton.closest("form")?.querySelector('input[name="from"]')?.focus();
      return;
    }
    const button = event.target.closest("[data-choice-preset]");
    if (!button) return;
    const form = button.closest("form");
    const name = button.dataset.choicePreset;
    if (!form || !name) return;
    const requested = button.dataset.values === "*"
      ? null
      : new Set(button.dataset.values.split(","));
    // History uses plain named checkboxes; Live still serializes through a
    // hidden CSV input until its rebuild.
    const checkboxes = form.querySelectorAll(
      `[data-list-filter="${name}"], input[type="checkbox"][name="${name}"]`,
    );
    checkboxes.forEach((checkbox) => {
      checkbox.checked = requested === null || requested.has(checkbox.value);
    });
    const hidden = form.querySelector(`[data-list-value="${name}"]`);
    if (hidden) {
      hidden.value = requested === null ? "" : Array.from(requested).join(",");
    }
    form.dispatchEvent(new Event("change", { bubbles: true }));
  });

  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || !form.matches("[data-export-form]")) {
      return;
    }
    const csrfSource = document.querySelector(
      '.header-actions form[action="/session/logout"] input[name="csrf_token"]',
    );
    if (csrfSource instanceof HTMLInputElement) {
      let csrfInput = form.querySelector('input[name="csrf_token"]');
      if (!(csrfInput instanceof HTMLInputElement)) {
        csrfInput = document.createElement("input");
        csrfInput.type = "hidden";
        csrfInput.name = "csrf_token";
        form.append(csrfInput);
      }
      csrfInput.value = csrfSource.value;
    }
    const status = document.querySelector("#history-export-status");
    if (status) {
      status.textContent =
        "Preparing the complete export. The download starts only after generation and auditing succeed.";
    }
    const dialog = form.closest("dialog");
    if (dialog instanceof HTMLDialogElement) dialog.close();
  });

  const updateHistoryCount = () => {
    let rows = Array.from(document.querySelectorAll("#history-rows .log-row"));
    let trimmed = false;
    if (rows.length > 1000) {
      const removed = rows.length - 1000;
      rows.slice(0, removed).forEach((row) => row.remove());
      rows = rows.slice(removed);
      trimmed = true;
    }
    const count = document.querySelector("[data-loaded-count]");
    if (count) count.textContent = String(rows.length);
    const label = document.querySelector("[data-loaded-label]");
    if (label) label.textContent = rows.length === 1 ? "event" : "events";
    if (trimmed) {
      // The cap removed the newest loaded rows from the top of the list. The
      // tail line becomes the recovery path back to the newest page of this
      // query; pagination in the older direction stays available.
      const tail = document.querySelector("#history-tail");
      if (tail && !tail.querySelector("[data-trim-recovery]")) {
        const canonical = tail.dataset.canonicalUrl || "/logs";
        const recover = document.createElement("a");
        recover.className = "tail-action";
        recover.href = canonical;
        recover.textContent = "Return to newest events";
        recover.setAttribute("data-trim-recovery", "");
        tail.querySelectorAll(".tail-action, #load-newer").forEach(
          (action) => action.remove(),
        );
        tail.insertBefore(recover, tail.lastElementChild);
      }
      const status = document.querySelector("#history-update-status");
      if (status) {
        status.textContent =
          "Browser row limit reached; the newest loaded rows were removed. Use the tail line to return to the newest events.";
      }
    }
  };

  const prepareResponsiveHistory = (root) => {
    if (!window.matchMedia("(max-width: 32rem)").matches) return;
    root.querySelectorAll(".history-filter-disclosure").forEach((details) => {
      details.removeAttribute("open");
    });
  };

  // Native datetime-local pickers for the UTC range fields. The named UTC
  // text input stays authoritative (and is the no-JS form, labelled UTC);
  // the picker is a synchronized view in the display zone, and the label
  // names that zone (DESIGN.md §10.2).
  const enhanceRangeInputs = (root) => {
    const time = window.siftailTime;
    root.querySelectorAll("input[data-utc-range]").forEach((utcInput) => {
      if (utcInput.dataset.rangeEnhanced) return;
      utcInput.dataset.rangeEnhanced = "true";
      const parsed = new Date(utcInput.value);
      if (Number.isNaN(parsed.getTime())) return;
      const label = utcInput.closest("label");
      label?.querySelectorAll("[data-range-zone]").forEach((zone) => {
        zone.textContent = time.zone;
      });
      const picker = document.createElement("input");
      picker.type = "datetime-local";
      picker.step = "1";
      picker.setAttribute(
        "aria-label",
        (label?.textContent || "").replace(/\s+/g, " ").trim(),
      );
      const help = utcInput.getAttribute("aria-describedby");
      if (help) picker.setAttribute("aria-describedby", help);
      picker.value = time.toWall(parsed);
      picker.addEventListener("change", () => {
        const instant = time.fromWall(picker.value);
        if (!instant) return;
        utcInput.value = instant.toISOString();
        utcInput.dispatchEvent(new Event("change", { bubbles: true }));
      });
      utcInput.hidden = true;
      utcInput.insertAdjacentElement("afterend", picker);
      utcInput.addEventListener("change", () => {
        const value = new Date(utcInput.value);
        if (!Number.isNaN(value.getTime())) picker.value = time.toWall(value);
      });
    });
  };

  // Reflect real request state on the History region (DESIGN.md §23.2).
  document.addEventListener("htmx:beforeRequest", (event) => {
    const region = document.querySelector("#history-region");
    if (region && region.contains(event.detail.elt)) {
      region.setAttribute("aria-busy", "true");
    }
  });
  document.addEventListener("htmx:afterRequest", () => {
    document.querySelector("#history-region")?.setAttribute("aria-busy", "false");
  });

  prepareResponsiveHistory(document);
  enhanceRangeInputs(document);
  document.addEventListener("htmx:afterSwap", (event) => {
    prepareResponsiveHistory(event.target);
    enhanceRangeInputs(event.target);
    updateHistoryCount();
    window.siftailKeyboard?.syncRows();
    if (event.target.matches?.(".event-detail-slot") &&
      event.target.firstElementChild) {
      const row = event.target.closest(".log-row");
      const toggle = row?.querySelector(".event-toggle");
      if (toggle) {
        toggle.setAttribute("aria-expanded", "true");
        toggle.textContent = "⌄";
      }
      event.target.querySelector(".event-detail")?.focus({ preventScroll: true });
    }
    const requester = event.detail.requestConfig?.elt?.id;
    if (requester === "load-older" && !document.querySelector("#load-older")) {
      document.querySelector(".result-summary")?.focus({ preventScroll: true });
    }
    if (requester === "load-newer") {
      const replacement = document.querySelector("#load-newer") ||
        document.querySelector("#history-tail");
      replacement?.focus({ preventScroll: true });
    }
  });

})();
