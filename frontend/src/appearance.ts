import colors from "./colors.json";
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
    ?.setAttribute(
      "content",
      colors[dark ? "dark" : "light"].background.default,
    );
  document.documentElement.setAttribute(dark ? "data-dark" : "data-light", "");
})();
