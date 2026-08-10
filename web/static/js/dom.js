// Small DOM helpers. Everything user- or API-supplied is inserted as text
// (textContent / attributes), never as HTML, so a title containing
// "<script>" is shown literally instead of being executed.

export const $ = (selector, root = document) => root.querySelector(selector);

/**
 * h builds an element: h("a", { href: "/x", class: "link" }, "text", child).
 * Attributes named on* are rejected so handlers are always attached with
 * addEventListener (inline handlers are also blocked by the server's CSP).
 */
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs)) {
    if (value === false || value === null || value === undefined) continue;
    if (name.startsWith("on")) throw new Error(`use addEventListener, not ${name}`);
    if (name === "class") el.className = value;
    else if (name === "dataset") Object.assign(el.dataset, value);
    else el.setAttribute(name, value === true ? "" : String(value));
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    el.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return el;
}

/** show/hide toggles the hidden attribute. */
export function show(el, visible = true) {
  el.hidden = !visible;
}

const longDate = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "long",
  year: "numeric",
  timeZone: "UTC",
});
const shortDate = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  year: "numeric",
  timeZone: "UTC",
});

/** APOD dates are calendar days; parse as UTC so no time zone shifts them. */
export const parseDay = (iso) => new Date(`${iso}T00:00:00Z`);
export const formatLong = (iso) => longDate.format(parseDay(iso));
export const formatShort = (iso) => shortDate.format(parseDay(iso));

/** A <time> element for an APOD date. */
export const timeEl = (iso, format = formatLong) => h("time", { datetime: iso }, format(iso));

/** "Today" for APOD is decided in US Eastern time, as on the server. */
export function apodToday() {
  // en-CA formats dates as YYYY-MM-DD.
  return new Intl.DateTimeFormat("en-CA", { timeZone: "America/New_York" }).format(new Date());
}

export const FIRST_DAY = "1995-06-16";

/** Days between two ISO dates, inclusive of both ends. */
export function daysInclusive(start, end) {
  return Math.round((parseDay(end) - parseDay(start)) / 86_400_000) + 1;
}

/** Wraps localStorage, which can throw (private mode, quota, disabled). */
export const storage = {
  get(key, fallback) {
    try {
      const raw = localStorage.getItem(key);
      return raw === null ? fallback : JSON.parse(raw);
    } catch {
      return fallback;
    }
  },
  set(key, value) {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch {
      /* best effort: the app still works without persistence */
    }
  },
  remove(key) {
    try {
      localStorage.removeItem(key);
    } catch {
      /* ignore */
    }
  },
};
