const status = document.getElementById("copy-status");

for (const button of document.querySelectorAll("[data-copy]")) {
  const field = document.getElementById(button.dataset.copy);
  if (!field) continue;

  button.hidden = false;
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(field.value);
      status.textContent = "Copied to clipboard.";
    } catch {
      field.focus();
      field.select();
      status.textContent = "Text selected. Use your device’s Copy command.";
    }
  });
}

const selectAll = document.getElementById("select-all-capabilities");

if (selectAll) {
  const options = Array.from(selectAll.form.querySelectorAll('input[name="capability"]'));

  function updateSelectAll() {
    const selected = options.filter(option => option.checked).length;
    selectAll.checked = selected === options.length;
    selectAll.indeterminate = selected > 0 && selected < options.length;
  }

  selectAll.addEventListener("change", () => {
    for (const option of options) option.checked = selectAll.checked;
    updateSelectAll();
  });
  for (const option of options) option.addEventListener("change", updateSelectAll);

  updateSelectAll();
  selectAll.closest("label").hidden = false;
}

const passwordToggle = document.getElementById("password-toggle");
if (passwordToggle) {
  const input = document.getElementById("password");
  passwordToggle.hidden = false;
  passwordToggle.addEventListener("click", () => {
    const visible = input.type === "password";
    input.type = visible ? "text" : "password";
    passwordToggle.textContent = visible ? "Hide" : "Show";
    passwordToggle.setAttribute("aria-label", visible ? "Hide password" : "Show password");
  });
}

const googleCode = document.getElementById("google-code");
if (googleCode) {
  const invite = document.getElementById("invite");
  googleCode.disabled = false;
  googleCode.form.addEventListener("submit", event => {
    if (!invite.reportValidity()) event.preventDefault();
    googleCode.value = invite.value;
  });
}
