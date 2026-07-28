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
})();
