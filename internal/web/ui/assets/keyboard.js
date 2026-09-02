"use strict";

// The single owner of keyboard shortcuts (DESIGN.md §32). Shortcuts are
// scoped by the data-kb-scope attribute the page declares on <main>: only
// "?" and Escape are global, everything else exists in the Logs workspace.
// Keys never fire while focus is in an input, select, textarea, or editable
// content, and every action has a visible clickable equivalent.
(() => {
  const scope = document.querySelector("main[data-kb-scope]")?.dataset.kbScope || "";

  const isTypingContext = (target) =>
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    Boolean(target?.isContentEditable);

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

  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || isTypingContext(event.target)) return;

    if (event.key === "?" && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      openHelp();
      return;
    }
    if (event.key === "Escape") {
      // Open dialogs and the System menu close themselves; otherwise clear
      // the active row selection.
      if (document.querySelector("dialog[open]")) return;
      const active = document.activeElement;
      if (active?.matches?.(".log-row")) {
        event.preventDefault();
        active.blur();
      }
      return;
    }

    if (scope !== "logs" || event.ctrlKey || event.metaKey || event.altKey) {
      return;
    }
    switch (event.key.toLowerCase()) {
      case "f":
        event.preventDefault();
        document.querySelector('input[name="contains"]')?.focus();
        return;
      case "l":
        event.preventDefault();
        modeLink("live")?.click();
        return;
      case "h":
        event.preventDefault();
        modeLink("history")?.click();
        return;
      case "j":
        event.preventDefault();
        moveRow(1);
        return;
      case "k":
        event.preventDefault();
        moveRow(-1);
        return;
      case "g":
        event.preventDefault();
        document.querySelector("[data-live-newest]")?.click();
        return;
      case " ":
        event.preventDefault();
        document.querySelector("[data-live-pause]")?.click();
        return;
      case "enter":
        if (document.activeElement?.matches?.(".log-row")) {
          event.preventDefault();
          document.activeElement.querySelector(".event-toggle")?.click();
        }
        return;
      default:
    }
  });
})();
