"use strict";

// The single owner of keyboard shortcuts and the only key-event listener in
// the interface (DESIGN.md §32, §39). Other modules register handlers here
// instead of adding their own listeners.
//
// Shortcuts are scoped by the data-kb-scope attribute the page declares on
// <main>: "?" and Escape are global, everything else exists in the Logs
// workspace. Printable single-key shortcuts never fire while focus is in an
// input, select, textarea, or editable content, never while a modal dialog
// is open, and never when the Keyboard shortcuts preference is off
// (data-shortcuts="off" on <html>, WCAG 2.1.4). Escape and Ctrl/Cmd+K always
// work. Every action has a visible clickable equivalent.
(() => {
  const root = document.documentElement;
  const scope = document.querySelector("main[data-kb-scope]")?.dataset.kbScope || "";
  const handlers = [];

  const shortcutsEnabled = () => root.dataset.shortcuts !== "off";

  const platform = navigator.userAgentData?.platform || navigator.platform || "";
  const isApple = /mac|iphone|ipad|ipod/i.test(platform);

  // The platform's spelling of a modifier chord: "⌘K" on Apple platforms,
  // "Ctrl K" elsewhere (DESIGN.md §6.1, §32.2).
  const chordLabel = (key) => (isApple ? `⌘${key}` : `Ctrl ${key}`);

  const isTypingContext = (target) =>
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    Boolean(target?.isContentEditable);

  // Space activates these natively (or scrolls the page from them); the
  // keyboard layer never takes it from them (WCAG 2.1.1).
  const ownsSpace = (target) =>
    target instanceof Element &&
    Boolean(target.closest(
      "button, a, summary, [role='button'], input, select, textarea, [contenteditable]",
    ));

  const modalOpen = () => Boolean(document.querySelector("dialog[open]"));

  const openHelp = () => {
    const dialog = document.querySelector("[data-keyboard-help]");
    if (dialog instanceof HTMLDialogElement && !dialog.open) {
      dialog.showModal();
    }
  };

  // ---- Log rows: one tab stop with a roving tabindex (DESIGN.md §32.3) ----
  //
  // The list (role=list) owns only rows (role=listitem). Exactly one row has
  // tabindex=0: the row the operator selected (data-row-selected), else the
  // newest — the first row in History, the last in Live. Every other row and
  // every detail toggle has tabindex=-1, so Tab enters the list once and
  // leaves it on the next press. Modules that add or remove rows call
  // syncRows().
  const rowContainer = () =>
    document.querySelector("#history-rows") || document.querySelector("#live-rows");

  const rowsOf = (list) => Array.from(list.children).filter(
    (child) => child.classList.contains("log-row"),
  );

  const newestRow = (list) => {
    const rows = rowsOf(list);
    return list.id === "live-rows" ? rows[rows.length - 1] : rows[0];
  };

  const rovingStop = (list) => list.querySelector(':scope > .log-row[tabindex="0"]');

  const syncRows = () => {
    const list = rowContainer();
    if (!list) return;
    const stop = rovingStop(list);
    if (stop?.hasAttribute("data-row-selected")) return;
    const newest = newestRow(list);
    if (stop === newest) return;
    if (stop) stop.tabIndex = -1;
    if (newest) newest.tabIndex = 0;
  };

  const selectRow = (row) => {
    const list = row.parentElement;
    const stop = list ? rovingStop(list) : null;
    if (stop && stop !== row) {
      stop.tabIndex = -1;
      stop.removeAttribute("data-row-selected");
    }
    row.tabIndex = 0;
    row.setAttribute("data-row-selected", "");
  };

  // The accessible name a focused row announces: its position and content
  // ("Row 12 of 200: ERR api/web, …"). It is set only while the row itself
  // has focus, because positions change as rows load and trim.
  const rowSummary = (row) => [
    row.querySelector(".severity")?.textContent,
    row.querySelector(".row-source")?.textContent,
    row.querySelector(".row-message")?.textContent?.slice(0, 120),
    row.querySelector("time")?.textContent,
    row.querySelector(".row-stream")?.textContent,
  ].map((part) => (part || "").trim()).filter(Boolean).join(", ");

  const labelRow = (row) => {
    const list = row.parentElement;
    if (!list) return;
    const rows = rowsOf(list);
    row.setAttribute(
      "aria-label",
      `Row ${rows.indexOf(row) + 1} of ${rows.length}: ${rowSummary(row)}`,
    );
  };

  const listRowOf = (target) => {
    const row = target instanceof Element ? target.closest(".log-row") : null;
    return row && row.parentElement === rowContainer() ? row : null;
  };

  document.addEventListener("focusin", (event) => {
    const row = listRowOf(event.target);
    if (!row) return;
    selectRow(row);
    if (event.target === row) labelRow(row);
  });
  document.addEventListener("focusout", (event) => {
    if (event.target instanceof Element && event.target.matches(".log-row")) {
      event.target.removeAttribute("aria-label");
    }
  });

  // Esc clears the selection: the tab stop returns to the newest row.
  const clearSelection = (row) => {
    row.removeAttribute("data-row-selected");
    row.blur();
    syncRows();
  };

  const moveRow = (delta) => {
    const list = rowContainer();
    if (!list) return;
    const rows = rowsOf(list);
    if (rows.length === 0) return;
    const current = rows.indexOf(listRowOf(document.activeElement));
    if (current < 0) {
      // Nothing focused in the list: resume at the tab stop.
      (rovingStop(list) || rows[0]).focus({ preventScroll: false });
      return;
    }
    const index = Math.max(0, Math.min(current + delta, rows.length - 1));
    rows[index].focus({ preventScroll: false });
  };

  // Keys the row list handles itself, like any composite widget: arrows on a
  // row or its toggle move the selection; Enter or Space on a focused row
  // toggles its details. They are not printable single-key shortcuts, so
  // they work when the shortcuts preference is off (§32.6). Keys inside an
  // expanded detail panel are left to its controls.
  const rowKey = (event) => {
    if (event.ctrlKey || event.metaKey || event.altKey || modalOpen()) return false;
    const row = listRowOf(event.target);
    if (!row) return false;
    const onRow = event.target === row;
    if (!onRow && !(event.target instanceof Element &&
      event.target.matches(".event-toggle"))) {
      return false;
    }
    switch (event.key) {
      case "ArrowDown":
        moveRow(1);
        return true;
      case "ArrowUp":
        moveRow(-1);
        return true;
      case "Enter":
      case " ":
        if (!onRow) return false;
        row.querySelector(".event-toggle")?.click();
        return true;
      default:
        return false;
    }
  };

  const modeLink = (mode) => {
    const links = document.querySelectorAll(".mode-switch a");
    for (const link of links) {
      const live = link.getAttribute("href")?.includes("mode=live");
      if ((mode === "live") === Boolean(live)) return link;
    }
    return null;
  };

  // Logs-workspace single-key shortcuts. Returns true when the key was
  // consumed; a key whose target control is absent is left alone.
  const logsShortcut = (event) => {
    switch (event.key.toLowerCase()) {
      case "f": {
        const search = document.querySelector('input[name="contains"]');
        if (!search) return false;
        search.focus();
        return true;
      }
      case "l": {
        const link = modeLink("live");
        if (!link) return false;
        link.click();
        return true;
      }
      case "h": {
        const link = modeLink("history");
        if (!link) return false;
        link.click();
        return true;
      }
      case "j":
        moveRow(1);
        return true;
      case "k":
        moveRow(-1);
        return true;
      case "g": {
        const newest = document.querySelector("[data-live-newest]");
        if (!newest) return false;
        newest.click();
        return true;
      }
      case " ": {
        // Bound only where a pause control exists (Live), and never over a
        // control that owns Space itself.
        const pause = document.querySelector("[data-live-pause]");
        if (!pause || ownsSpace(event.target)) return false;
        pause.click();
        return true;
      }
      default:
        return false;
    }
  };

  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented) return;

    // Registered handlers run first: they own keys inside their own
    // components (the palette input, the System menu) and global chords.
    for (const handler of handlers) {
      if (handler(event)) {
        event.preventDefault();
        return;
      }
    }

    if (isTypingContext(event.target)) return;

    if (rowKey(event)) {
      event.preventDefault();
      return;
    }

    if (event.key === "Escape") {
      // Open dialogs and the System menu close themselves; otherwise clear
      // the active row selection.
      if (modalOpen()) return;
      const active = document.activeElement;
      if (active?.matches?.(".log-row")) {
        event.preventDefault();
        clearSelection(active);
      }
      return;
    }

    if (!shortcutsEnabled() || modalOpen() ||
      event.ctrlKey || event.metaKey || event.altKey) {
      return;
    }

    if (event.key === "?") {
      event.preventDefault();
      openHelp();
      return;
    }

    if (scope === "logs" && logsShortcut(event)) {
      event.preventDefault();
    }
  });

  // Visible platform-correct chord labels for the palette button and help.
  document.querySelectorAll("[data-chord-key]").forEach((element) => {
    element.textContent = chordLabel(element.dataset.chordKey);
  });

  // The System menu's Keyboard shortcuts item opens the help even when the
  // single-key "?" is off. It needs this module, so it is revealed here.
  document.querySelectorAll("[data-open-keyboard-help]").forEach((button) => {
    button.hidden = false;
    button.addEventListener("click", () => {
      const menu = button.closest("details");
      if (menu instanceof HTMLDetailsElement) menu.open = false;
      openHelp();
    });
  });

  window.siftailKeyboard = Object.freeze({
    // register(handler): handler(event) returns true when it consumed the
    // key; the keyboard module then prevents the default. Handlers see every
    // keydown, including those from text fields, and decide for themselves.
    register: (handler) => {
      if (typeof handler === "function") handlers.push(handler);
    },
    shortcutsEnabled,
    chordLabel,
    // syncRows(): re-establish the row list's tab stop after rows were
    // added, removed, or replaced.
    syncRows,
  });
  syncRows();
})();
