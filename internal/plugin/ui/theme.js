// Apply Silo's launch theme and readability settings before the first paint.
(() => {
  const root = document.documentElement;
  const key = "hdhomerun-silo-theme";
  const slug = (value) => (/^[a-z0-9_-]{1,64}$/.test(value || "") ? value : "");
  try {
    const launch = slug(root.getAttribute("data-silo-theme"));
    const saved = slug(localStorage.getItem(key));
    root.setAttribute("data-silo-theme", launch || saved || "midnight-cinema");
    if (launch) localStorage.setItem(key, launch);
    const owner = localStorage.getItem("silo-ui-cache-owner") || "device";
    const cached = (name) => localStorage.getItem(name + ":" + owner);
    const scale = cached("silo-ui-text-scale");
    root.dataset.textScale = ["large", "x-large"].includes(scale)
      ? scale
      : "default";
    root.dataset.textWeight =
      cached("silo-ui-text-weight") === "strong" ? "strong" : "default";
    root.dataset.highContrast =
      cached("silo-ui-high-contrast") === "true" ? "true" : "false";
  } catch (_) {
    if (!root.getAttribute("data-silo-theme"))
      root.setAttribute("data-silo-theme", "midnight-cinema");
  }
})();
