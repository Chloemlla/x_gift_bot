// Runs before the body is painted. Uses the same key and selector as ThemeProvider.
(() => {
  let mode = "system";
  try {
    mode = localStorage.getItem("xgift-mode") || mode;
  } catch {
    /* Storage may be unavailable. */
  }
  const dark =
    mode === "dark" ||
    (mode !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute("content", dark ? "#111318" : "#F5F5F7");
  document.documentElement.setAttribute(dark ? "data-dark" : "data-light", "");
})();
