// scd dashboard script. Intentionally tiny and dependency-free.
"use strict";

// Stack checkboxes save as soon as they change. Saving only changes the selection;
// nothing deploys until an Apply.
document.addEventListener("change", (e) => {
  const box = e.target.closest(".select input[type=checkbox]");
  if (box) box.form.submit();
});
