(() => {
  "use strict";

  function initConnections() {
    const testButton = document.getElementById("btn-test-conn");
    const result = document.getElementById("test-result");
    const form = document.getElementById("connection-form");

    if (testButton && result && form) {
      testButton.addEventListener("click", async () => {
        result.textContent = "Menguji koneksi…";
        result.dataset.state = "loading";
        testButton.disabled = true;

        try {
          const formData = new FormData(form);
          const response = await fetch("/admin/connections/test", {
            method: "POST",
            headers: { "Content-Type": "application/x-www-form-urlencoded" },
            body: new URLSearchParams(formData).toString(),
            credentials: "same-origin",
          });
          const payload = await response.json();
          if (!response.ok || !payload.success) {
            result.textContent = `Gagal: ${payload.message || "Koneksi tidak berhasil diuji."}`;
            result.dataset.state = "error";
            return;
          }
          result.textContent = `Berhasil: ${payload.message || "Koneksi dapat digunakan."}`;
          result.dataset.state = "success";
        } catch (_error) {
          result.textContent = "Gagal menguji koneksi. Periksa jaringan dan coba lagi.";
          result.dataset.state = "error";
        } finally {
          testButton.disabled = false;
        }
      });
    }

    document.querySelectorAll("form.delete-form").forEach((formElement) => {
      if (!(formElement instanceof HTMLFormElement)) return;
      if (!formElement.dataset.confirm) {
        formElement.dataset.confirm = `Hapus profil koneksi "${formElement.dataset.name || "ini"}"? Tindakan ini tidak dapat dibatalkan.`;
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initConnections);
  } else {
    initConnections();
  }
})();
