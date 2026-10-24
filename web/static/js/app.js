(() => {
  "use strict";

  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;

    const submitter = event.submitter;
    const message =
      (submitter && submitter.dataset && submitter.dataset.confirm) ||
      form.dataset.confirm;
    if (!message) return;

    if (!window.confirm(message)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  });

  document.addEventListener("click", (event) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const trigger = target.closest("a[data-confirm], button[type='button'][data-confirm]");
    if (!trigger) return;
    const message = trigger.getAttribute("data-confirm");
    if (!message) return;
    if (!window.confirm(message)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  });
})();
