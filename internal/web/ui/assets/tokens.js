(() => {
  "use strict";

  const workspace = document.querySelector("[data-one-time-token]");
  if (!workspace) return;
  const secret = workspace.querySelector("[data-token-secret]");
  const toggle = workspace.querySelector("[data-token-toggle]");
  const copy = workspace.querySelector("[data-token-copy]");
  const status = workspace.querySelector("[data-token-status]");
  const doneURL = workspace.dataset.doneUrl;
  if (!(secret instanceof HTMLInputElement) ||
      !(toggle instanceof HTMLButtonElement) ||
      !(copy instanceof HTMLButtonElement) ||
      !(status instanceof HTMLElement) ||
      !doneURL) return;

  history.replaceState(null, "", doneURL);

  toggle.addEventListener("click", () => {
    const showing = secret.type === "text";
    secret.type = showing ? "password" : "text";
    toggle.textContent = showing ? "Show" : "Hide";
    toggle.setAttribute("aria-pressed", String(!showing));
    secret.focus();
  });

  copy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(secret.value);
      status.textContent = "Token copied.";
    } catch {
      status.textContent = "Copy failed. Select the token and copy it manually.";
      secret.type = "text";
      secret.focus();
      secret.select();
    }
  });

  window.addEventListener("pagehide", () => {
    secret.value = "";
    status.textContent = "";
  }, { once: true });
})();
