"use strict";

// The command palette (DESIGN.md §32.4). Commands are harvested from the
// rendered page — navigation links, the mode switch, source filter selects,
// and page actions — so every entry reflects something the page can really
// do. No network requests, no inline scripts, nothing destructive: entries
// only navigate, set a filter control, or press a visible button.
(() => {
  const dialog = document.querySelector("[data-command-palette]");
  if (!(dialog instanceof HTMLDialogElement)) return;
  const input = dialog.querySelector("[data-palette-input]");
  const list = dialog.querySelector("[data-palette-results]");
  const emptyNote = dialog.querySelector("[data-palette-empty]");
  if (!input || !list) return;

  let commands = [];
  let matches = [];
  let activeIndex = 0;
  let returnFocus = null;

  const collectCommands = () => {
    const collected = [];
    document.querySelectorAll(".primary-nav a, .system-menu-nav a").forEach((link) => {
      collected.push({
        label: link.textContent.trim(),
        kind: "Go to",
        run: () => window.location.assign(link.href),
      });
    });
    document.querySelectorAll(".mode-switch a").forEach((link) => {
      collected.push({
        label: link.textContent.trim(),
        kind: "Switch to",
        run: () => window.location.assign(link.href),
      });
    });
    if (document.querySelector("[data-open-export]")) {
      collected.push({
        label: "Export matching History",
        kind: "Action",
        run: () => document.querySelector("[data-open-export]")?.click(),
      });
    }
    document.querySelectorAll(
      ".source-filters select, select[name='source']",
    ).forEach((select) => {
      const dimension = select.closest("label")?.firstChild?.textContent?.trim() ||
        "Source";
      select.querySelectorAll("option").forEach((option) => {
        if (!option.value) return;
        collected.push({
          label: option.textContent.trim(),
          kind: dimension,
          run: () => {
            select.value = option.value;
            select.dispatchEvent(new Event("change", { bubbles: true }));
          },
        });
      });
    });
    return collected;
  };

  // Subsequence fuzzy score: every query character must appear in order;
  // word-start and adjacency matches rank higher.
  const score = (query, label) => {
    if (!query) return 1;
    const q = query.toLowerCase();
    const l = label.toLowerCase();
    let qi = 0;
    let total = 0;
    let streak = 0;
    for (let li = 0; li < l.length && qi < q.length; li += 1) {
      if (l[li] === q[qi]) {
        streak += 1;
        total += streak;
        if (li === 0 || l[li - 1] === " " || l[li - 1] === "/") total += 3;
        qi += 1;
      } else {
        streak = 0;
      }
    }
    return qi === q.length ? total : 0;
  };

  const render = () => {
    const query = input.value.trim();
    matches = commands
      .map((command) => ({ command, rank: score(query, command.label) }))
      .filter((entry) => entry.rank > 0)
      .sort((a, b) => b.rank - a.rank)
      .slice(0, 12)
      .map((entry) => entry.command);
    activeIndex = 0;
    list.replaceChildren();
    matches.forEach((command, index) => {
      const item = document.createElement("li");
      item.id = `palette-option-${index}`;
      item.setAttribute("role", "option");
      item.setAttribute("aria-selected", index === activeIndex ? "true" : "false");
      const kind = document.createElement("span");
      kind.className = "palette-kind";
      kind.textContent = command.kind;
      const label = document.createElement("span");
      label.className = "palette-label";
      label.textContent = command.label;
      item.append(kind, label);
      item.addEventListener("mousedown", (event) => {
        event.preventDefault();
        execute(index);
      });
      list.append(item);
    });
    if (emptyNote) emptyNote.hidden = matches.length > 0;
    syncActive();
  };

  const syncActive = () => {
    list.querySelectorAll("[role='option']").forEach((item, index) => {
      item.setAttribute("aria-selected", index === activeIndex ? "true" : "false");
    });
    // An empty aria-activedescendant points at nothing; remove it instead.
    if (matches.length > 0) {
      input.setAttribute("aria-activedescendant", `palette-option-${activeIndex}`);
    } else {
      input.removeAttribute("aria-activedescendant");
    }
    // The popup listbox is expanded only while it shows options.
    input.setAttribute("aria-expanded", matches.length > 0 ? "true" : "false");
    list.querySelector(`#palette-option-${activeIndex}`)?.scrollIntoView({
      block: "nearest",
    });
  };

  const execute = (index) => {
    const command = matches[index];
    dialog.close();
    command?.run();
  };

  const open = () => {
    if (dialog.open) return;
    returnFocus = document.activeElement;
    commands = collectCommands();
    input.value = "";
    dialog.showModal();
    render();
    input.focus();
  };

  dialog.addEventListener("close", () => {
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
    if (returnFocus instanceof HTMLElement && document.contains(returnFocus)) {
      returnFocus.focus({ preventScroll: true });
    }
  });

  input.addEventListener("input", render);

  // Keys reach the palette through the keyboard module, the only key-event
  // listener (DESIGN.md §32.6, §39).
  const keyboard = window.siftailKeyboard;
  if (!keyboard) return;

  keyboard.register((event) => {
    if (event.target !== input) return false;
    switch (event.key) {
      case "ArrowDown":
        if (matches.length > 0) activeIndex = (activeIndex + 1) % matches.length;
        syncActive();
        return true;
      case "ArrowUp":
        if (matches.length > 0) {
          activeIndex = (activeIndex - 1 + matches.length) % matches.length;
        }
        syncActive();
        return true;
      case "Enter":
        if (matches.length > 0) execute(activeIndex);
        return true;
      default:
        return false;
    }
  });

  // Ctrl/Cmd+K is a modifier chord, not a printable single-key shortcut, so
  // it works whatever the shortcuts preference says (§32.6). It is claimed
  // only here, where the palette can actually open.
  keyboard.register((event) => {
    if (!(event.ctrlKey || event.metaKey) || event.altKey ||
      event.key.toLowerCase() !== "k") {
      return false;
    }
    open();
    return true;
  });

  const paletteButton = document.querySelector("[data-palette-open]");
  if (paletteButton) {
    paletteButton.hidden = false;
    paletteButton.addEventListener("click", open);
  }
})();
