document.addEventListener('DOMContentLoaded', () => {
  const testBtn = document.getElementById('btn-test-conn');
  if (testBtn) {
    testBtn.addEventListener('click', () => {
      const resultSpan = document.getElementById('test-result');
      if (!resultSpan) return;

      resultSpan.textContent = 'Testing connectivity...';
      resultSpan.style.color = '#164e8a';

      const form = document.getElementById('connection-form');
      if (!form) return;

      const formData = new FormData(form);

      fetch('/admin/connections/test', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: new URLSearchParams(formData).toString(),
      })
        .then((res) => res.json())
        .then((data) => {
          if (data.success) {
            resultSpan.textContent = '✓ ' + data.message;
            resultSpan.style.color = '#166534';
          } else {
            resultSpan.textContent = '✗ ' + data.message;
            resultSpan.style.color = '#991b1b';
          }
        })
        .catch(() => {
          resultSpan.textContent = '✗ Network error during connection test.';
          resultSpan.style.color = '#991b1b';
        });
    });
  }

  const deleteForms = document.querySelectorAll('.delete-form');
  deleteForms.forEach((form) => {
    form.addEventListener('submit', (e) => {
      const name = form.getAttribute('data-name') || 'this connection';
      if (!confirm(`Are you sure you want to delete profile "${name}"?`)) {
        e.preventDefault();
      }
    });
  });
});
