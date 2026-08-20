(() => {
  "use strict";

  const root = document.documentElement;
  const shortcutsDialog = () => document.querySelector("[data-shortcuts-dialog]");
  const searchInput = () => document.querySelector("[data-search-input]");
  let goPrefixUntil = 0;

  const setSidebar = (open) => {
    root.classList.toggle("sidebar-open", open);
    document.querySelectorAll(".menu-button").forEach((button) => {
      button.setAttribute("aria-expanded", String(open));
      button.setAttribute("aria-label", open ? "Close navigation" : "Open navigation");
    });
  };

  const setSearch = (open) => {
    root.classList.toggle("search-open", open);
    document.querySelectorAll("[data-search-toggle]").forEach((button) => {
      button.setAttribute("aria-expanded", String(open));
      button.setAttribute("aria-label", open ? "Close search" : "Open search");
    });
    if (open) window.requestAnimationFrame(() => searchInput()?.focus());
  };

  const showShortcuts = () => {
    const shortcuts = shortcutsDialog();
    if (!shortcuts) return;
    if (typeof shortcuts.showModal === "function") shortcuts.showModal();
    else shortcuts.setAttribute("open", "");
  };

  const closeShortcuts = () => {
    const shortcuts = shortcutsDialog();
    if (!shortcuts?.open) return;
    if (typeof shortcuts.close === "function") shortcuts.close();
    else shortcuts.removeAttribute("open");
  };

  const isTyping = (target) =>
    target instanceof HTMLElement &&
    (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));

  const focusThread = (direction) => {
    const rows = [...document.querySelectorAll("[data-thread-row]")];
    if (!rows.length) return;
    const active = document.activeElement?.closest?.("[data-thread-row]");
    const selected = document.querySelector("[data-thread-row].selected");
    const current = active || selected;
    const index = current ? rows.indexOf(current) : direction > 0 ? -1 : 0;
    const next = rows[Math.min(rows.length - 1, Math.max(0, index + direction))];
    next?.focus({ preventScroll: true });
    next?.scrollIntoView({ block: "nearest" });
  };

  const goToFolder = (key) => {
    const destinations = {
      i: "/inbox",
      s: "/sent",
      d: "/drafts",
      a: "/archive",
      t: "/trash",
    };
    if (destinations[key]) {
      const destination = new URL(destinations[key], window.location.origin);
      const mailbox = new URLSearchParams(window.location.search).get("mailbox");
      if (mailbox) destination.searchParams.set("mailbox", mailbox);
      window.location.assign(`${destination.pathname}${destination.search}`);
    }
  };

  const restoreSubmitting = (form) => {
    if (!(form instanceof HTMLFormElement)) return;
    delete form.dataset.submitting;
    form.removeAttribute("aria-busy");
    form.querySelectorAll(".is-pending").forEach((button) => {
      button.classList.remove("is-pending");
      button.removeAttribute("aria-disabled");
    });
  };

  const scheduleToast = () => {
    const toast = document.querySelector("[data-toast]");
    if (toast && !toast.dataset.dismissScheduled) {
      toast.dataset.dismissScheduled = "true";
      window.setTimeout(() => toast.remove(), 6000);
    }
  };

  document.addEventListener("click", (event) => {
    if (event.target.closest("[data-sidebar-toggle]")) {
      setSidebar(!root.classList.contains("sidebar-open"));
      return;
    }
    if (event.target.closest("[data-search-toggle]")) {
      setSearch(!root.classList.contains("search-open"));
      return;
    }
    if (event.target.closest("[data-shortcuts-open]")) {
      showShortcuts();
      return;
    }
    const toastButton = event.target.closest("[data-dismiss-toast]");
    if (toastButton) toastButton.closest("[data-toast]")?.remove();
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      setSidebar(false);
      setSearch(false);
      closeShortcuts();
      return;
    }
    if (event.metaKey || event.ctrlKey || event.altKey || isTyping(event.target)) return;

    const key = event.key.toLowerCase();
    if (Date.now() < goPrefixUntil) {
      goPrefixUntil = 0;
      if (["i", "s", "d", "a", "t"].includes(key)) {
        event.preventDefault();
        goToFolder(key);
      }
      return;
    }
    if (key === "g") {
      goPrefixUntil = Date.now() + 1200;
      return;
    }
    if (key === "?") {
      event.preventDefault();
      showShortcuts();
    } else if (key === "/") {
      event.preventDefault();
      setSearch(true);
    } else if (key === "c") {
      const compose = document.querySelector(".compose-button");
      if (compose) {
        event.preventDefault();
        compose.click();
      }
    } else if (key === "r") {
      const reply = document.querySelector("[data-shortcut-reply]");
      if (reply) {
        event.preventDefault();
        reply.click();
      }
    } else if (key === "j") {
      event.preventDefault();
      focusThread(1);
    } else if (key === "k") {
      event.preventDefault();
      focusThread(-1);
    }
  });

  document.addEventListener("submit", (event) => {
    const form = event.target;
    const message = event.submitter?.dataset.confirm || form.dataset.confirm;
    if (message && !window.confirm(message)) {
      event.preventDefault();
      return;
    }
    if (form.dataset.submitting === "true") {
      event.preventDefault();
      return;
    }
    form.dataset.submitting = "true";
    form.setAttribute("aria-busy", "true");
    const submitter = event.submitter;
    if (submitter?.dataset.pendingLabel) {
      submitter.classList.add("is-pending");
      submitter.setAttribute("aria-disabled", "true");
    }
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

  document.addEventListener("htmx:afterSwap", () => {
    setSidebar(false);
    setSearch(false);
    scheduleToast();
  });
  document.addEventListener("htmx:responseError", (event) => restoreSubmitting(event.detail.elt?.closest?.("form") || event.detail.elt));
  document.addEventListener("htmx:sendError", (event) => restoreSubmitting(event.detail.elt?.closest?.("form") || event.detail.elt));

  scheduleToast();
})();
