"use strict";

(() => {
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
  if (prefsForm) {
    prefsForm.addEventListener("change", () => {
      const data = new FormData(prefsForm);
      applyTheme(String(data.get("theme")));
      applyDensity(String(data.get("density")));
      void fetch(prefsForm.action, {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
        },
        body: new URLSearchParams(data).toString(),
        credentials: "same-origin",
        redirect: "manual",
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
    systemMenu.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && systemMenu.open) {
        systemMenu.open = false;
        const summary = systemMenu.querySelector("summary");
        if (summary) summary.focus();
      }
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
  // text input stays authoritative (and is the no-JS form); the picker is a
  // synchronized convenience view in the browser's local timezone.
  const enhanceRangeInputs = (root) => {
    root.querySelectorAll("input[data-utc-range]").forEach((utcInput) => {
      if (utcInput.dataset.rangeEnhanced) return;
      utcInput.dataset.rangeEnhanced = "true";
      const parsed = new Date(utcInput.value);
      if (Number.isNaN(parsed.getTime())) return;
      const picker = document.createElement("input");
      picker.type = "datetime-local";
      picker.step = "1";
      picker.setAttribute("aria-label", "Local time picker");
      const toLocalValue = (date) => {
        const shifted = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
        return shifted.toISOString().slice(0, 19);
      };
      picker.value = toLocalValue(parsed);
      picker.addEventListener("change", () => {
        const local = new Date(picker.value);
        if (Number.isNaN(local.getTime())) return;
        utcInput.value = local.toISOString();
        utcInput.dispatchEvent(new Event("change", { bubbles: true }));
      });
      utcInput.hidden = true;
      utcInput.insertAdjacentElement("afterend", picker);
      utcInput.addEventListener("change", () => {
        const value = new Date(utcInput.value);
        if (!Number.isNaN(value.getTime())) picker.value = toLocalValue(value);
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

  localizeTimes(document);
  prepareResponsiveHistory(document);
  enhanceRangeInputs(document);
  document.addEventListener("htmx:afterSwap", (event) => {
    localizeTimes(event.target);
    prepareResponsiveHistory(event.target);
    enhanceRangeInputs(event.target);
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
