"use strict";

(() => {
  const storage = {
    get(key, fallback) {
      try {
        return window.localStorage.getItem(key) || fallback;
      } catch {
        return fallback;
      }
    },
    set(key, value) {
      try {
        window.localStorage.setItem(key, value);
      } catch {
        // Preferences remain usable for this page when storage is unavailable.
      }
    },
  };

  const themeSelect = document.querySelector("[data-theme-select]");
  const densitySelect = document.querySelector("[data-density-select]");
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

  const theme = storage.get("siftail.theme", "system");
  const density = storage.get("siftail.density", "compact");
  applyTheme(theme);
  applyDensity(density);

  if (themeSelect) {
    themeSelect.value = theme;
    themeSelect.addEventListener("change", () => {
      applyTheme(themeSelect.value);
      storage.set("siftail.theme", themeSelect.value);
    });
  }
  if (densitySelect) {
    densitySelect.value = density;
    densitySelect.addEventListener("change", () => {
      applyDensity(densitySelect.value);
      storage.set("siftail.density", densitySelect.value);
    });
  }

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

  document.addEventListener("click", (event) => {
    const collapse = event.target.closest("[data-collapse-detail]");
    if (collapse) {
      const slot = collapse.closest(".event-detail-slot");
      const row = slot?.closest(".log-row");
      const toggle = row?.querySelector(".event-toggle");
      slot?.replaceChildren();
      if (toggle) {
        toggle.setAttribute("aria-expanded", "false");
        toggle.textContent = "›";
        toggle.focus({ preventScroll: true });
      }
      return;
    }
    const copy = event.target.closest("[data-copy-target]");
    if (copy) {
      const source = document.getElementById(copy.dataset.copyTarget);
      if (!source || !navigator.clipboard) return;
      const original = copy.textContent;
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
    const expandedToggle = event.target.closest(".event-toggle[aria-expanded='true']");
    if (expandedToggle) {
      event.preventDefault();
      event.stopPropagation();
      const slot = document.getElementById(expandedToggle.getAttribute("aria-controls"));
      slot?.replaceChildren();
      expandedToggle.setAttribute("aria-expanded", "false");
      expandedToggle.textContent = "›";
      expandedToggle.focus({ preventScroll: true });
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
    const checkboxes = form.querySelectorAll(`[data-list-filter="${name}"]`);
    checkboxes.forEach((checkbox) => {
      checkbox.checked = requested === null || requested.has(checkbox.value);
    });
    const hidden = form.querySelector(`[data-list-value="${name}"]`);
    if (!hidden) return;
    hidden.value = requested === null ? "" : Array.from(requested).join(",");
    hidden.dispatchEvent(new Event("change", { bubbles: true }));
  });

  const localizeTimes = (root) => {
    root.querySelectorAll("[data-local-time]").forEach((element) => {
      const date = new Date(element.dateTime);
      if (Number.isNaN(date.getTime())) return;
      const options = {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        fractionalSecondDigits: 3,
      };
      if (element.hasAttribute("data-show-date")) {
        options.day = "2-digit";
        options.month = "short";
      }
      element.textContent = new Intl.DateTimeFormat([], options).format(date);
      element.title = date.toLocaleString();
    });
  };

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
    if (rows.length >= 1000) {
      document.querySelector("#history-pagination")?.remove();
      document.querySelector("[data-more-summary]")?.remove();
      const status = document.querySelector("#history-update-status");
      if (status) {
        status.textContent = trimmed
          ? "Browser row limit reached; earlier loaded rows were removed. Refine the filters to inspect another interval."
          : "Browser row limit reached. Refine the filters to inspect another interval.";
      }
    }
  };

  const prepareResponsiveHistory = (root) => {
    if (!window.matchMedia("(max-width: 32rem)").matches) return;
    root.querySelectorAll(".history-filter-disclosure").forEach((details) => {
      details.removeAttribute("open");
    });
  };

  localizeTimes(document);
  prepareResponsiveHistory(document);
  document.addEventListener("htmx:afterSwap", (event) => {
    localizeTimes(event.target);
    prepareResponsiveHistory(event.target);
    updateHistoryCount();
    if (event.target.matches?.(".event-detail-slot")) {
      const row = event.target.closest(".log-row");
      const toggle = row?.querySelector(".event-toggle");
      if (toggle) {
        toggle.setAttribute("aria-expanded", "true");
        toggle.textContent = "⌄";
      }
      event.target.querySelector(".event-detail")?.focus({ preventScroll: true });
    }
    if (
      event.detail.requestConfig?.elt?.id === "load-older" &&
      !document.querySelector("#load-older")
    ) {
      document.querySelector(".result-summary")?.focus({ preventScroll: true });
    }
  });

  document.addEventListener("keydown", (event) => {
    const target = event.target;
    if (
      target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement ||
      target?.isContentEditable
    ) {
      return;
    }
    if (event.key.toLowerCase() === "f" && !event.ctrlKey && !event.metaKey) {
      event.preventDefault();
      document.querySelector('input[name="contains"]')?.focus();
      return;
    }
    if (
      event.key === "/" ||
      ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k")
    ) {
      event.preventDefault();
      document.querySelector('select[name="server"]')?.focus();
    }
  });
})();
