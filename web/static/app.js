(() => {
  "use strict";

  const root = document.documentElement;
  const setSidebar = (open) => {
    root.classList.toggle("sidebar-open", open);
    document.querySelectorAll(".menu-button").forEach((button) => {
      button.setAttribute("aria-expanded", String(open));
      button.setAttribute("aria-label", open ? "Close navigation" : "Open navigation");
    });
  };
  const toggleSidebar = () => setSidebar(!root.classList.contains("sidebar-open"));

  document.addEventListener("click", (event) => {
    if (event.target.closest("[data-sidebar-toggle]")) toggleSidebar();
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") setSidebar(false);
  });

  document.addEventListener("submit", (event) => {
    const message = event.submitter?.dataset.confirm || event.target.dataset.confirm;
    if (message && !window.confirm(message)) event.preventDefault();
  });

  document.addEventListener("change", (event) => {
	if (!event.target.matches("[data-auto-submit]")) return;
	if (event.target.type === "file" && !event.target.files?.length) return;
	event.target.form?.requestSubmit();
  });

  document.addEventListener("htmx:configRequest", (event) => {
    const token = document.querySelector('meta[name="csrf-token"]')?.content;
    if (token) event.detail.headers["X-CSRF-Token"] = token;
  });

  document.addEventListener("htmx:afterSwap", () => setSidebar(false));
})();
