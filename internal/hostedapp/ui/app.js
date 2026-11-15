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
