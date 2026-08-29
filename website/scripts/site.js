(() => {
  const RELEASE_CACHE_KEY = "kenn-forge-site:release";
  const RELEASE_CACHE_MS = 60 * 60 * 1000;

  async function latestRelease() {
    try {
      const cached = JSON.parse(localStorage.getItem(RELEASE_CACHE_KEY) || "null");
      if (cached && Date.now() - cached.fetchedAt < RELEASE_CACHE_MS) {
        return cached.release;
      }
    } catch {
      // fall through to a fresh fetch
    }
    const response = await fetch(
      "https://api.github.com/repos/kenn-io/forge/releases/latest",
      { headers: { Accept: "application/vnd.github+json" } },
    );
    if (!response.ok) {
      throw new Error(`release lookup failed: ${response.status}`);
    }
    const release = await response.json();
    try {
      localStorage.setItem(
        RELEASE_CACHE_KEY,
        JSON.stringify({ fetchedAt: Date.now(), release: { tag_name: release.tag_name } }),
      );
    } catch {
      // storage unavailable; version still renders this visit
    }
    return release;
  }

  async function showVersion() {
    try {
      const release = await latestRelease();
      if (!release || !release.tag_name) {
        return;
      }
      for (const target of document.querySelectorAll("[data-version]")) {
        target.textContent = release.tag_name;
      }
    } catch {
      // static release links keep working without a version label
    }
  }

  function installLightbox() {
    const lightbox = document.querySelector("dialog.image-lightbox");
    if (!lightbox || typeof lightbox.showModal !== "function") {
      return;
    }
    lightbox.addEventListener("click", () => lightbox.close());
    for (const trigger of document.querySelectorAll(".image-zoom")) {
      const image = trigger.querySelector("img");
      if (!image) {
        continue;
      }
      trigger.addEventListener("click", () => {
        lightbox.replaceChildren();
        const zoomed = document.createElement("img");
        zoomed.src = image.currentSrc || image.src;
        zoomed.alt = image.alt;
        lightbox.append(zoomed);
        lightbox.showModal();
      });
    }
  }

  showVersion();
  installLightbox();
})();
