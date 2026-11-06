// Copy text to the clipboard on plain HTTP as well as HTTPS.
//
// navigator.clipboard only exists in secure contexts, and the panel is
// usually opened over plain HTTP, so a clipboard-only copy silently
// fails there. Fall back to the classic execCommand path, which works
// from a user gesture on HTTP too. Returns false only when both paths
// fail, so callers can show the manual-copy hint as a last resort.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Fall through to execCommand below.
  }
  try {
    const el = document.createElement("textarea");
    el.value = text;
    el.setAttribute("readonly", "");
    el.style.position = "fixed";
    el.style.opacity = "0";
    document.body.appendChild(el);
    el.focus();
    el.select();
    const ok = document.execCommand("copy");
    el.remove();
    return ok;
  } catch {
    return false;
  }
}
