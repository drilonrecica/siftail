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

  const rowContainer = () =>
    document.querySelector("#history-rows") || document.querySelector("#live-rows");

  const moveRow = (delta) => {
    const container = rowContainer();
    if (!container) return;
    const available = Array.from(container.querySelectorAll(".log-row"));
    if (available.length === 0) return;
    const current = available.indexOf(document.activeElement);
    const index = delta > 0
      ? Math.min(current + 1, available.length - 1)
      : Math.max(current < 0 ? available.length - 1 : current - 1, 0);
    available[index].focus({ preventScroll: false });
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
      case "enter":
        if (document.activeElement?.matches?.(".log-row")) {
          document.activeElement.querySelector(".event-toggle")?.click();
          return true;
        }
        return false;
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

    if (event.key === "Escape") {
      // Open dialogs and the System menu close themselves; otherwise clear
      // the active row selection.
      if (modalOpen()) return;
      const active = document.activeElement;
      if (active?.matches?.(".log-row")) {
        event.preventDefault();
        active.blur();
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
  });
})();
