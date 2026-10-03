/**
 * Copies text to the clipboard from a click, wherever the back office is
 * opened. Returns false when the browser would not copy, so the caller can
 * select the text and ask for Cmd+C instead.
 *
 * `navigator.clipboard` only exists in secure contexts: https, or localhost.
 * The back office opened on a phone or another computer at
 * http://192.168.x.x:3000 has none, so the old way (select a hidden textarea,
 * execCommand("copy")) runs instead. It must run while the click is still
 * being handled, which is why it comes before any await on that path.
 */
export async function copyText(text: string): Promise<boolean> {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Refused (permissions, or the page lost focus): try the old way.
    }
  }
  return legacyCopy(text);
}

function legacyCopy(text: string): boolean {
  if (typeof document === "undefined") return false;
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", ""); // no keyboard pops up on phones
  area.style.position = "fixed";
  area.style.top = "0";
  area.style.opacity = "0";
  document.body.appendChild(area);
  try {
    area.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
  }
}

/** Selects an element's text, so the person can copy it with Cmd+C or Ctrl+C. */
export function selectText(el: Element) {
  const range = document.createRange();
  range.selectNodeContents(el);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
}
