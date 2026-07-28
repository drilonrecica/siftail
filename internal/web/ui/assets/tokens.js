(() => {
  "use strict";

  const workspace = document.querySelector("[data-one-time-token]");
  if (!workspace) return;
  const secret = workspace.querySelector("[data-token-secret]");
  const toggle = workspace.querySelector("[data-token-toggle]");
  const copy = workspace.querySelector("[data-token-copy]");
  const status = workspace.querySelector("[data-token-status]");
  const doneURL = workspace.dataset.doneUrl;
  const placeholder = workspace.dataset.tokenPlaceholder;
  if (!(secret instanceof HTMLInputElement) ||
      !(toggle instanceof HTMLButtonElement) ||
      !(copy instanceof HTMLButtonElement) ||
      !(status instanceof HTMLElement) ||
      !doneURL ||
      !placeholder) return;

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

  const materials = new Map();
  workspace.querySelectorAll("[data-token-material]").forEach((material) => {
    if (material instanceof HTMLElement && material.dataset.materialName) {
      materials.set(material.dataset.materialName, material);
    }
  });
  workspace.querySelectorAll("[data-material-copy]").forEach((button) => {
    if (!(button instanceof HTMLButtonElement)) return;
    button.addEventListener("click", async () => {
      const material = materials.get(button.dataset.materialCopy);
      if (!(material instanceof HTMLElement)) return;
      const complete = material.textContent.split(placeholder).join(secret.value);
      try {
        await navigator.clipboard.writeText(complete);
        const label = button.textContent.replace(/^Copy /, "");
        status.textContent = `${label} copied with the one-time token.`;
      } catch {
        status.textContent = "Copy failed. Copy the token and replace the placeholder manually.";
        secret.type = "text";
        secret.focus();
        secret.select();
      }
    });
  });

  const testForm = workspace.querySelector("[data-guide-test]");
  const testButton = workspace.querySelector("[data-guide-test-button]");
  const testResult = workspace.querySelector("[data-guide-result]");
  const stageItems = new Map();
  workspace.querySelectorAll("[data-guide-stage]").forEach((item) => {
    if (item instanceof HTMLElement && item.dataset.guideStage) {
      stageItems.set(item.dataset.guideStage, item);
    }
  });

  const setStage = (name, state) => {
    const item = stageItems.get(name);
    const label = item?.querySelector("span");
    if (!(item instanceof HTMLElement) || !(label instanceof HTMLElement)) return;
    item.dataset.state = state;
    label.textContent = state === "passed" ? "Passed" :
      state === "failed" ? "Failed" :
      state === "pending" ? "Not confirmed" : "Not tested";
  };

  if (testForm instanceof HTMLFormElement &&
      testButton instanceof HTMLButtonElement &&
      testResult instanceof HTMLElement) {
    testButton.addEventListener("click", async () => {
      stageItems.forEach((_item, name) => setStage(name, "pending"));
      testButton.disabled = true;
      testResult.textContent = "Sending the bounded test event…";
      const values = new URLSearchParams(new FormData(testForm));
      values.set("token", secret.value);
      try {
        const response = await fetch(testForm.action, {
          method: "POST",
          headers: {"Content-Type": "application/x-www-form-urlencoded"},
          body: values,
          credentials: "same-origin",
          redirect: "follow",
        });
        if (response.redirected || !response.headers.get("Content-Type")?.startsWith("application/json")) {
          window.location.assign(response.url || "/login?expired=1");
          return;
        }
        const result = await response.json();
        testResult.textContent = `${result.title}. ${result.detail}`;
        if (result.outcome === "committed") {
          stageItems.forEach((_item, name) => setStage(name, "passed"));
        } else if (result.outcome === "authentication-rejected") {
          setStage("delivery", "passed");
          setStage("authentication", "failed");
        } else if (result.outcome === "delivery-failed") {
          setStage("delivery", "failed");
        } else {
          setStage("delivery", "passed");
        }
      } catch {
        setStage("delivery", "failed");
        testResult.textContent = "The guided test could not complete. Check the browser session and try again.";
      } finally {
        testButton.disabled = false;
      }
    });
  }

  window.addEventListener("pagehide", () => {
    secret.value = "";
    status.textContent = "";
    materials.forEach((material) => {
      material.textContent = "";
    });
    if (testResult instanceof HTMLElement) testResult.textContent = "";
  }, { once: true });
})();
