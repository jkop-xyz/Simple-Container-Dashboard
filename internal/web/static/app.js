// scd dashboard script. Intentionally tiny and dependency-free.
"use strict";

// Stack checkboxes save as soon as they change. Saving only changes the selection;
// nothing deploys until an Apply.
document.addEventListener("change", (e) => {
  const box = e.target.closest(".select input[type=checkbox]");
  if (box) box.form.submit();
});

// Follow a running Apply's output live, then reload to show the results.
const applyOutput = document.querySelector(".apply-output[data-stream]");
if (applyOutput) {
  const already = applyOutput.textContent.split("\n").filter((l, i, a) => i < a.length - 1).length;
  const es = new EventSource(`${applyOutput.dataset.stream}?from=${already}`);
  es.onmessage = (e) => {
    applyOutput.textContent += e.data + "\n";
    applyOutput.scrollTop = applyOutput.scrollHeight;
  };
  es.addEventListener("done", () => { es.close(); location.reload(); });
}
